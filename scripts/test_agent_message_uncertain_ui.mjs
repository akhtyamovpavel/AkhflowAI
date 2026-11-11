import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { setTimeout as delay } from "node:timers/promises";
import { pathToFileURL } from "node:url";

const moduleName = process.env.PLAYWRIGHT_MODULE;
const defaultModule = new URL("../web/node_modules/playwright-core/index.mjs", import.meta.url).href;
const playwright = await import(moduleName ? pathToFileURL(moduleName).href : defaultModule);
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
let releaseResponse;
const responseWait = new Promise((resolve) => { releaseResponse = resolve; });
let releaseLateResponse;
const lateResponseWait = new Promise((resolve) => { releaseLateResponse = resolve; });
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
  assert.ok(ready, "Vite did not start");
  browser = await playwright.chromium.launch({ executablePath: "/usr/bin/google-chrome", headless: true });
  const page = await browser.newPage();
  const token = `fixture.${Buffer.from('{"sub":"1"}').toString("base64url")}.fixture`;
  const otherToken = `fixture.${Buffer.from('{"sub":"2"}').toString("base64url")}.fixture`;
  await page.addInitScript((initialToken) => {
    if (!sessionStorage.getItem("fixture.initialized")) {
      localStorage.setItem("akhflow.token", initialToken);
      localStorage.setItem("akhflow.project", "alpha");
      sessionStorage.setItem("fixture.initialized", "yes");
    }
  }, token);

  let posts = 0;
  let failHistory = false;
  let saved = false;
  let postStarted;
  const firstPost = new Promise((resolve) => { postStarted = resolve; });
  let latePostStarted;
  const latePost = new Promise((resolve) => { latePostStarted = resolve; });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Альфа" }, { id: 2, slug: "beta", name: "Бета" }];
    } else if (path === "/api/projects/alpha/chats" || path === "/api/projects/beta/chats") {
      data = [{ id: 1, title: "Первый чат" }, { id: 2, title: "Второй чат" }];
    } else if (path === "/api/chats/1/messages" && request.method() === "POST") {
      posts++;
      if (posts === 1) {
        const markerExists = await page.evaluate(() => {
          return Object.keys(sessionStorage).some((key) => key.includes("akhflow.pendingAgentMessage"));
        });
        assert.ok(markerExists, "marker must be persisted before POST");
        saved = true;
        postStarted();
        await responseWait;
        await route.abort("connectionreset").catch(() => {});
        return;
      }
      if (posts === 3) {
        latePostStarted();
        await lateResponseWait;
        await route.fulfill({ status: 200, json: { reply: "Запоздалый ответ" } }).catch(() => {});
        return;
      }
      data = { reply: "Новый ответ" };
    } else if (path === "/api/chats/1/messages") {
      if (failHistory) {
        await route.fulfill({ status: 503, json: { error: "unavailable" } }).catch(() => {});
        return;
      }
      data = saved ? [{ id: 10, role: "user", content: "Действие первому" }] : [];
    }
    await route.fulfill({ status: 200, json: data }).catch(() => {});
  });

  await page.goto(url, { timeout: 15000 });
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  const input = page.getByPlaceholder("Скажи агенту, что сделать...");
  await input.fill("Действие первому");
  await input.press("Enter");
  await firstPost;
  await input.press("Enter");
  assert.equal(posts, 1, "double submit must not send another POST");
  releaseResponse();
  await page.getByRole("alert").filter({ hasText: "Результат отправки неизвестен" }).waitFor();
  await input.fill("Новый черновик");
  await input.press("Enter");
  assert.equal(posts, 1, "unknown outcome must block another POST");

  await page.getByRole("button", { name: "Второй чат" }).click();
  assert.equal(await page.getByText("Результат отправки неизвестен", { exact: false }).count(), 0);
  await page.getByRole("button", { name: "Первый чат" }).click();
  await page.getByRole("alert").filter({ hasText: "Результат отправки неизвестен" }).waitFor();
  assert.equal(posts, 1, "switching chats must not resend the message");

  await page.reload({ timeout: 15000 });
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await page.getByRole("alert").filter({ hasText: "Результат отправки неизвестен" }).waitFor();
  const confirm = page.getByRole("button", { name: "Я проверил историю и понимаю, что действие может продолжаться" });
  assert.equal(await confirm.count(), 0, "automatic history load must not release the guard");
  failHistory = true;
  await page.getByRole("button", { name: "Проверить историю" }).click();
  await page.getByText("Не удалось загрузить историю чата").waitFor();
  assert.equal(await confirm.count(), 0, "failed history check must not enable acknowledgement");
  failHistory = false;
  await page.getByRole("button", { name: "Проверить историю" }).click();
  await confirm.waitFor();
  assert.equal(posts, 1, "history checks must never repeat POST");

  await page.evaluate((nextToken) => localStorage.setItem("akhflow.token", nextToken), otherToken);
  await page.reload({ timeout: 15000 });
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await page.getByRole("button", { name: "Первый чат" }).waitFor();
  assert.equal(await page.getByText("Результат отправки неизвестен", { exact: false }).count(), 0);
  await page.evaluate((nextToken) => localStorage.setItem("akhflow.token", nextToken), token);
  await page.reload({ timeout: 15000 });
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await page.getByRole("alert").filter({ hasText: "Результат отправки неизвестен" }).waitFor();
  await page.getByRole("button", { name: "Проверить историю" }).click();
  await confirm.click();
  await page.getByRole("alert").filter({ hasText: "Результат отправки неизвестен" }).waitFor({ state: "hidden" });
  await page.getByText("Предыдущее действие с неизвестным результатом всё ещё может выполняться", { exact: false }).waitFor();
  assert.equal(posts, 1, "acknowledging must never send another POST");
  await input.fill("Новый вопрос");
  await input.press("Enter");
  await page.getByText("Новый ответ").waitFor();
  assert.equal(posts, 2);
  const markers = await page.evaluate(() => Object.keys(sessionStorage).filter((key) => key.includes("akhflow.pendingAgentMessage") && !key.endsWith(".acknowledged")));
  assert.equal(markers.length, 0, "a successful response must clear its marker");

  await input.fill("Поздний ответ");
  await input.press("Enter");
  await latePost;
  await page.locator("header select").first().selectOption("beta");
  await page.getByRole("button", { name: "Первый чат" }).waitFor();
  assert.equal(await page.getByText("Результат отправки неизвестен", { exact: false }).count(), 0);
  releaseLateResponse();
  await page.locator("header select").first().selectOption("alpha");
  await page.getByRole("alert").filter({ hasText: "Результат отправки неизвестен" }).waitFor();
  assert.equal(await page.getByText("Запоздалый ответ").count(), 0, "late response must not affect a remounted project");
  assert.equal(posts, 3, "project changes must not repeat POST");
  await page.getByRole("button", { name: "Проверить историю" }).click();
  await confirm.click();

  await page.addInitScript(() => {
    const originalSetItem = Storage.prototype.setItem;
    Storage.prototype.setItem = function (key, value) {
      if (this === sessionStorage && key.includes("akhflow.pendingAgentMessage")) throw new Error("storage unavailable");
      return originalSetItem.call(this, key, value);
    };
  });
  await page.reload({ timeout: 15000 });
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await page.getByText("Предыдущее действие с неизвестным результатом всё ещё может выполняться", { exact: false }).waitFor();
  await input.fill("Не отправлять без маркера");
  await input.press("Enter");
  await page.getByRole("alert").filter({ hasText: "Не удалось сохранить состояние отправки" }).waitFor();
  assert.equal(posts, 3, "failed marker persistence must prevent POST");
  console.log("PASS: uncertain agent sends require explicit history review and do not repeat automatically");
} finally {
  releaseResponse();
  releaseLateResponse();
  if (browser) await browser.close();
  server.kill();
}
