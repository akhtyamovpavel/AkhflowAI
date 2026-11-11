import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { setTimeout as delay } from "node:timers/promises";
import { pathToFileURL } from "node:url";

const defaultModule = new URL("../web/node_modules/playwright-core/index.mjs", import.meta.url).href;
const playwright = await import(process.env.PLAYWRIGHT_MODULE ? pathToFileURL(process.env.PLAYWRIGHT_MODULE).href : defaultModule);
const probe = createServer();
await new Promise((resolve) => probe.listen(0, "127.0.0.1", resolve));
const port = probe.address().port;
await new Promise((resolve) => probe.close(resolve));
const url = `http://127.0.0.1:${port}`;
const server = spawn("./node_modules/.bin/vite", ["--host", "127.0.0.1", "--port", String(port), "--strictPort"], {
  cwd: new URL("../web", import.meta.url),
  stdio: "ignore",
});
let browser;
let page;
try {
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      if ((await fetch(url)).ok) break;
    } catch {
      await delay(100);
    }
  }
  browser = await playwright.chromium.launch({ executablePath: "/usr/bin/google-chrome", headless: true });
  page = await browser.newPage();
  const token = `fixture.${Buffer.from('{"sub":"1"}').toString("base64url")}.fixture`;
  const otherToken = `fixture.${Buffer.from('{"sub":"2"}').toString("base64url")}.fixture`;
  await page.addInitScript((value) => {
    if (!sessionStorage.getItem("initialized")) {
      localStorage.setItem("akhflow.token", value);
      localStorage.setItem("akhflow.project", "alpha");
      sessionStorage.setItem("initialized", "1");
    }
  }, token);
  const posts = [];
  let response = { status: 503, json: { error: "unavailable" } };
  let historyResponse = null;
  let runList = [];
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let data = [];
    if (path === "/api/projects") data = [{ id: 1, slug: "alpha", name: "Alpha" }, { id: 2, slug: "beta", name: "Beta" }];
    if (path.endsWith("/chats")) data = [{ id: 1, title: "Первый чат" }, { id: 2, title: "Второй чат" }];
    if (path === "/api/chats/1/messages" && historyResponse) {
      await route.fulfill(historyResponse);
      return;
    }
    if (path === "/api/chats/1/runs" && request.method() === "GET") data = runList;
    if (path === "/api/chats/1/runs" && request.method() === "POST") {
      const key = request.headers()["idempotency-key"];
      if (response === "accepted") {
        await delay(500);
      }
      const marker = await page.evaluate(() => {
        const name = Object.keys(localStorage).find((item) => item.includes("akhflow.pendingAgentMessage") && !item.endsWith(".acknowledged"));
        return name ? JSON.parse(localStorage.getItem(name)) : null;
      });
      assert.equal(marker.id, key, "marker must be stored before POST");
      posts.push({ key, text: request.postDataJSON().message });
      if (response === "accepted") {
        data = { id: 1, chat_id: 1, request_key: key, status: "queued" };
        await route.fulfill({ status: 202, json: data });
      } else {
        await route.fulfill(response);
      }
      return;
    }
    await route.fulfill({ status: 200, json: data });
  });
  await page.goto(url);
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  const input = page.getByPlaceholder("Скажи агенту, что сделать...");
  const alert = page.getByRole("alert").filter({ hasText: "Результат отправки неизвестен" });
  const check = page.getByRole("button", { name: "Проверить запуск" });
  const retry = page.getByRole("button", { name: "Повторить проверенную отправку" });
  const retrySend = async () => {
    const accepted = page.waitForResponse((result) => {
      const request = result.request();
      return new URL(result.url()).pathname === "/api/chats/1/runs" && request.method() === "POST" && result.status() === 202;
    });
    await Promise.all([accepted, retry.click()]);
    await page.waitForFunction(() => {
      return !Object.keys(localStorage).some((name) => name.includes("akhflow.pendingAgentMessage") && !name.endsWith(".acknowledged"));
    });
    await alert.waitFor({ state: "hidden" });
  };
  await input.fill("Первое действие");
  await input.press("Enter");
  await alert.waitFor();
  await input.fill("Новый черновик");
  await input.press("Enter");
  assert.equal(posts.length, 1);
  await page.getByRole("button", { name: "Второй чат" }).click();
  await page.getByRole("button", { name: "Первый чат" }).click();
  await alert.waitFor();
  await page.reload();
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await alert.waitFor();
  assert.equal(await retry.count(), 0);
  for (const invalid of [
    { status: 503, json: {} },
    { status: 200, body: "null", contentType: "application/json" },
    { status: 200, json: { messages: [] } },
    { status: 200, json: [{ id: 10, role: "user" }] },
    { status: 202, json: [] },
  ]) {
    historyResponse = invalid;
    await check.click();
    await page.getByText("Не удалось загрузить историю чата").waitFor();
    assert.equal(await retry.count(), 0, "invalid history must not enable retry");
  }
  historyResponse = null;
  for (const invalid of [[null], [{ id: 1, steps: {} }]]) {
    runList = invalid;
    await check.click();
    await delay(150);
    assert.equal(await retry.count(), 0, "invalid run list must not enable retry");
  }
  runList = [];
  await check.click();
  await retry.waitFor();
  historyResponse = { status: 200, json: null };
  await check.click();
  await page.getByText("Не удалось загрузить историю чата").waitFor();
  assert.equal(await retry.count(), 0, "later invalid history must revoke retry");
  historyResponse = null;
  await page.evaluate((value) => localStorage.setItem("akhflow.token", value), otherToken);
  await page.reload();
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  assert.equal(await alert.count(), 0, "marker must stay scoped to the account");
  await page.evaluate((value) => localStorage.setItem("akhflow.token", value), token);
  await page.reload();
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await alert.waitFor();
  await check.click();
  response = "accepted";
  await retrySend();
  assert.equal(posts[0].key, posts[1].key, "explicit retry must use original key");
  const malformed = [
    { status: 200, json: { id: 1, chat_id: 1, status: "queued" } },
    { status: 202, json: null },
    { status: 202, json: { status: "pending_or_unknown" } },
    { status: 202, json: { id: -1, chat_id: 1, status: "queued" } },
    { status: 202, body: "{broken", contentType: "application/json" },
  ];
  for (const invalid of malformed) {
    response = invalid;
    await input.fill("Запрос с ошибочным ответом");
    await input.press("Enter");
    await alert.waitFor();
    const count = posts.length;
    await input.fill("Сохранённый черновик");
    await input.press("Enter");
    assert.equal(posts.length, count, "malformed response must keep guard");
    await check.click();
    response = "accepted";
    await retrySend();
    assert.equal(posts[count - 1].key, posts[count].key);
    assert.equal(await input.inputValue(), "Сохранённый черновик");
  }
  for (const storageName of ["sessionStorage", "localStorage"]) {
    await page.evaluate((name) => {
      const storageKey = JSON.stringify(["akhflow.pendingAgentMessage", "1", "alpha", 1]);
      const marker = { id: "00000000-0000-4000-8000-000000000001", text: "Старое действие" };
      window[name].setItem(storageKey, JSON.stringify(marker));
    }, storageName);
    await page.reload();
    await page.getByRole("button", { name: "Агент", exact: true }).click();
    await alert.waitFor();
    await check.click();
    const acknowledge = page.getByRole("button", { name: "Я проверил историю и понимаю, что действие может продолжаться" });
    await acknowledge.waitFor();
    assert.equal(await retry.count(), 0, "legacy marker must never retry through runs");
    const count = posts.length;
    await acknowledge.click();
    await alert.waitFor({ state: "hidden" });
    assert.equal(posts.length, count, "legacy acknowledgement must not send an action");
  }
  await page.evaluate(() => {
    const key = JSON.stringify(["akhflow.pendingAgentMessage", "1", "alpha", 1]);
    sessionStorage.setItem(key, JSON.stringify({ id: "old", text: "Старое действие" }));
    localStorage.setItem(key, JSON.stringify({ id: "new", text: "Новое действие", kind: "run" }));
  });
  await page.reload();
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await page.getByText("Не удалось сохранить состояние отправки", { exact: false }).waitFor();
  const blockedPosts = posts.length;
  await input.fill("Не отправлять");
  await input.press("Enter");
  assert.equal(posts.length, blockedPosts, "two pending markers must block sends");
  await page.evaluate(() => {
    const key = JSON.stringify(["akhflow.pendingAgentMessage", "1", "alpha", 1]);
    sessionStorage.removeItem(key);
    localStorage.removeItem(key);
  });
  await page.addInitScript(() => {
    const original = Storage.prototype.setItem;
    Storage.prototype.setItem = function (key, value) {
      if (this === localStorage && key.includes("akhflow.pendingAgentMessage")) throw new Error("storage unavailable");
      return original.call(this, key, value);
    };
  });
  await page.reload();
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  const before = posts.length;
  await input.fill("Без сохранения не отправлять");
  await input.press("Enter");
  await page.getByText("Не удалось сохранить состояние отправки", { exact: false }).waitFor();
  assert.equal(posts.length, before);
  console.log("PASS: uncertain sends, malformed responses, reload, account isolation and storage failure");
} finally {
  if (page) await page.unrouteAll({ behavior: "wait" });
  if (browser) await browser.close();
  server.kill();
}
