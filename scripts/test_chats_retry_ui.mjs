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
    try { ready = (await fetch(url)).ok; } catch { await delay(100); }
    if (ready) break;
  }
  assert.ok(ready, 'Vite did not start');
  browser = await playwright.chromium.launch({ executablePath: '/usr/bin/google-chrome', headless: true });
  const context = await browser.newContext();
  await context.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/projects') return route.fulfill({ json: [{ id: 1, slug: 'alpha', name: 'Alpha' }] });
    if (path.endsWith('/role')) return route.fulfill({ json: { role: 'owner' } });
    if (path.endsWith('/events')) return route.fulfill({ status: 403, body: '' });
    if (path.endsWith('/chats') && route.request().method() === 'POST') return route.fulfill({ json: { id: 91 } });
    if (path.includes('/agent/usage')) return route.fulfill({ json: { requests: 0, replies: 0, tool_calls: 0 } });
    return route.fulfill({ json: [] });
  });
  const page = await context.newPage();
  page.setDefaultTimeout(10000);
  await page.addInitScript(() => {
    localStorage.setItem('akhflow.token', 'fixture-a');
    const original = window.fetch.bind(window);
    window.chatMode = 'fail';
    window.chatRequests = [];
    window.fetch = (input, options = {}) => {
      if (String(input).endsWith('/api/projects/alpha/chats') && (!options.method || options.method === 'GET')) {
        const response = (status, title = 'Existing chat') => new Response(
          JSON.stringify(status === 200 ? [{ id: 91, title }] : { error: 'unavailable' }),
          { status, headers: { 'Content-Type': 'application/json' } },
        );
        if (window.chatMode === 'hold') {
          return new Promise((resolve) => window.chatRequests.push({ release: (status, title) => resolve(response(status, title)) }));
        }
        return Promise.resolve(response(window.chatMode === 'fail' ? 503 : 200));
      }
      return original(input, options);
    };
  });
  await page.goto(url, { timeout: 20000 });
  await page.getByRole('button', { name: 'Агент', exact: true }).click();
  const alert = page.getByRole('alert').filter({ hasText: 'Не удалось загрузить чаты' });
  await alert.waitFor();
  assert.equal(await page.getByText('Нет чатов', { exact: true }).count(), 0);
  assert.equal(await page.evaluate(() => localStorage.getItem('akhflow.token')), 'fixture-a');
  await page.evaluate(() => { window.chatMode = 'hold'; });
  await alert.getByRole('button', { name: 'Повторить' }).click();
  await page.waitForFunction(() => window.chatRequests.length === 1);
  assert.equal(await alert.getByRole('button', { name: 'Повторить' }).isDisabled(), true);
  await page.evaluate(() => window.chatRequests.shift().release(503));
  await page.waitForFunction(() => !document.querySelector('[role="alert"] button')?.disabled);
  await page.evaluate(() => { window.chatMode = 'success'; });
  await alert.getByRole('button', { name: 'Повторить' }).click();
  await page.getByRole('button', { name: 'Existing chat' }).waitFor();
  assert.equal(await alert.count(), 0);
  await page.evaluate(() => { window.chatMode = 'hold'; });
  await page.getByText('Чаты', { exact: true }).locator('..').getByRole('button').click();
  await page.waitForFunction(() => window.chatRequests.length === 1);
  await page.evaluate(() => window.chatRequests.shift().release(503));
  await alert.waitFor();
  await page.getByRole('button', { name: 'Existing chat' }).waitFor();
  await alert.getByRole('button', { name: 'Повторить' }).click();
  await page.waitForFunction(() => window.chatRequests.length === 1);
  await page.evaluate(() => {
    window.chatMode = 'success';
    localStorage.setItem('akhflow.token', 'fixture-b');
    window.dispatchEvent(new StorageEvent('storage', { key: 'akhflow.token', storageArea: localStorage }));
  });
  await page.waitForFunction(() => localStorage.getItem('akhflow.token') === 'fixture-b');
  await page.evaluate(() => window.chatRequests.shift().release(401));
  await delay(200);
  assert.equal(await page.evaluate(() => localStorage.getItem('akhflow.token')), 'fixture-b');
  console.log('PASS: chat list failure and retry');
} finally {
  if (browser) await browser.close();
  server.kill('SIGTERM');
}
