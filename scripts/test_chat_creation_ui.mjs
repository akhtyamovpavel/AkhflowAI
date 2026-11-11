import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createServer } from 'node:net';
import { setTimeout as delay } from 'node:timers/promises';
import { pathToFileURL } from 'node:url';

const modulePath = process.env.PLAYWRIGHT_MODULE;
const defaultModule = new URL('../web/node_modules/playwright-core/index.mjs', import.meta.url).href;
const playwright = await import(modulePath ? pathToFileURL(modulePath).href : defaultModule);
const probe = createServer();
await new Promise((resolve) => probe.listen(0, '127.0.0.1', resolve));
const port = probe.address().port;
await new Promise((resolve) => probe.close(resolve));
const url = `http://127.0.0.1:${port}`;
const server = spawn('./node_modules/.bin/vite', ['--host', '127.0.0.1', '--port', String(port), '--strictPort'], {
  cwd: new URL('../web', import.meta.url),
  stdio: 'ignore',
});
let browser;
try {
  let ready = false;
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      ready = (await fetch(url)).ok;
    } catch {
      await delay(100);
    }
    if (ready) break;
  }
  assert.ok(ready, 'Vite did not start');
  browser = await playwright.chromium.launch({ executablePath: '/usr/bin/google-chrome', headless: true });
  const context = await browser.newContext();
  await context.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/projects') {
      return route.fulfill({ json: [{ id: 1, slug: 'alpha', name: 'Alpha' }, { id: 2, slug: 'beta', name: 'Beta' }] });
    }
    if (path.endsWith('/role')) return route.fulfill({ json: { role: 'owner' } });
    if (path.endsWith('/events')) return route.fulfill({ status: 403, body: '' });
    if (path.includes('/agent/usage')) return route.fulfill({ json: { requests: 0, replies: 0, tool_calls: 0 } });
    return route.fulfill({ json: [] });
  });
  const page = await context.newPage();
  await page.addInitScript(() => {
    const payload = btoa(JSON.stringify({ sub: '1' })).replace(/=/g, '');
    localStorage.setItem('akhflow.token', `header.${payload}.signature`);
    const original = window.fetch.bind(window);
    const chats = JSON.parse(sessionStorage.getItem('fixture-chats') || '{}');
    const existingIds = Object.values(chats).map((chat) => chat.id);
    const nextId = Math.max(90, ...existingIds) + 1;
    window.chatFixture = { mode: 'hold', requests: [], chats, nextId, release: null };
    const json = (value, status = 200) => new Response(JSON.stringify(value), {
      status,
      headers: { 'Content-Type': 'application/json' },
    });
    window.fetch = (input, options = {}) => {
      const match = String(input).match(/\/api\/projects\/([^/]+)\/chats$/);
      if (!match) return original(input, options);
      const project = match[1];
      const fixture = window.chatFixture;
      if (!options.method || options.method === 'GET') {
        const chats = Object.values(fixture.chats).filter((chat) => chat.project === project);
        return Promise.resolve(json(chats));
      }
      const key = options.headers['Idempotency-Key'];
      fixture.requests.push({ key, project });
      if (fixture.mode === 'fail') return Promise.resolve(json({ error: 'unavailable' }, 503));
      const scope = `${project}:${key}`;
      if (!fixture.chats[scope]) fixture.chats[scope] = { id: fixture.nextId++, title: '', project };
      sessionStorage.setItem('fixture-chats', JSON.stringify(fixture.chats));
      const chat = fixture.chats[scope];
      if (fixture.mode === 'lost') return Promise.reject(new TypeError('connection lost'));
      if (fixture.mode === 'late401') {
        return new Promise((resolve) => {
          fixture.release = () => resolve(json({ error: 'expired' }, 401));
        });
      }
      if (fixture.mode === 'hold') {
        return new Promise((resolve, reject) => {
          fixture.release = () => resolve(json({ id: chat.id }, 201));
          options.signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')));
        });
      }
      return Promise.resolve(json({ id: chat.id }, 201));
    };
  });
  await page.goto(url, { timeout: 20000 });
  await page.getByRole('button', { name: 'Агент', exact: true }).click();
  const create = page.getByRole('button', { name: 'Создать чат' });
  await create.waitFor();
  await create.evaluate((button) => { button.click(); button.click(); });
  await page.waitForFunction(() => window.chatFixture.requests.length === 1);
  assert.equal(await create.isDisabled(), true);
  await page.evaluate(() => window.chatFixture.release());
  await page.getByRole('button', { name: 'Чат #91' }).waitFor();
  assert.equal(await create.isEnabled(), true);

  await page.evaluate(() => { window.chatFixture.mode = 'fail'; });
  await create.click();
  const alert = page.getByRole('alert').filter({ hasText: 'Не удалось подтвердить создание чата' });
  await alert.waitFor();
  assert.equal(await create.isDisabled(), true);
  await page.evaluate(() => { window.chatFixture.mode = 'ok'; });
  await alert.getByRole('button', { name: 'Повторить создание' }).click();
  await page.getByRole('button', { name: 'Чат #92' }).waitFor();
  const keys = await page.evaluate(() => window.chatFixture.requests.map((request) => request.key));
  assert.equal(keys[1], keys[2], 'retry must reuse the same key');
  assert.notEqual(keys[0], keys[1], 'new creation must use a new key');

  await page.evaluate(() => { window.chatFixture.mode = 'lost'; });
  await create.click();
  await alert.waitFor();
  const savedKey = await page.evaluate(() => window.chatFixture.requests.at(-1).key);
  const storageKey = JSON.stringify(['akhflow.pendingChatCreation', '1', 'alpha']);
  assert.equal(await page.evaluate((name) => sessionStorage.getItem(name), storageKey), savedKey);
  await page.reload({ timeout: 20000 });
  await page.getByRole('button', { name: 'Агент', exact: true }).click();
  await alert.waitFor();
  assert.equal(await create.isDisabled(), true);
  await page.evaluate(() => { window.chatFixture.mode = 'ok'; });
  await alert.getByRole('button', { name: 'Повторить создание' }).click();
  await page.waitForFunction((name) => sessionStorage.getItem(name) === null, storageKey);
  const retriedKey = await page.evaluate(() => window.chatFixture.requests.at(-1).key);
  assert.equal(retriedKey, savedKey, 'reload must retain the original key');
  assert.equal(await page.evaluate(() => Object.keys(window.chatFixture.chats).length), 3);
  assert.equal(await alert.count(), 0);

  const countBeforeStorageFailure = await page.evaluate(() => window.chatFixture.requests.length);
  await page.evaluate(() => {
    window.originalSetItem = Storage.prototype.setItem;
    Storage.prototype.setItem = function (name, value) {
      if (name.includes('akhflow.pendingChatCreation')) throw new DOMException('Storage denied', 'SecurityError');
      return window.originalSetItem.call(this, name, value);
    };
  });
  await create.click();
  const storageAlert = page.getByRole('alert').filter({ hasText: 'Проверь доступ к хранилищу браузера' });
  await storageAlert.waitFor();
  assert.equal(await page.evaluate(() => window.chatFixture.requests.length), countBeforeStorageFailure);
  await page.evaluate(() => { Storage.prototype.setItem = window.originalSetItem; });
  await storageAlert.getByRole('button', { name: 'Повторить создание' }).click();
  await page.waitForFunction((count) => window.chatFixture.requests.length === count + 1, countBeforeStorageFailure);
  await page.getByRole('button', { name: 'Чат #94' }).waitFor();

  await page.evaluate(() => { window.chatFixture.mode = 'late401'; });
  await create.click();
  await page.waitForFunction(() => window.chatFixture.requests.length === 3);
  const switchedKey = await page.evaluate(() => window.chatFixture.requests.at(-1).key);
  await page.locator('header select').first().selectOption('beta');
  await page.getByRole('button', { name: 'Создать чат' }).waitFor();
  await page.evaluate(() => window.chatFixture.release());
  await delay(100);
  assert.ok(await page.evaluate(() => localStorage.getItem('akhflow.token')));
  assert.equal(await page.getByRole('alert').filter({ hasText: 'Не удалось подтвердить создание чата' }).count(), 0);
  await page.evaluate(() => { window.chatFixture.mode = 'ok'; });
  await page.locator('header select').first().selectOption('alpha');
  await alert.waitFor();
  await alert.getByRole('button', { name: 'Повторить создание' }).click();
  await page.waitForFunction((key) => window.chatFixture.requests.at(-1).key === key, switchedKey);
  await page.getByRole('button', { name: 'Чат #95' }).waitFor();
  console.log('PASS: chat creation guards and idempotent retry');
} finally {
  if (browser) await browser.close();
  server.kill('SIGTERM');
}
