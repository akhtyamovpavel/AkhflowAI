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
let releaseSecond;
const delayedSecond = new Promise((resolve) => { releaseSecond = resolve; });
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
  await page.addInitScript(() => {
    if (!sessionStorage.getItem("fixture.initialized")) {
      localStorage.setItem("akhflow.token", "fixture-token");
      localStorage.setItem("akhflow.project", "alpha");
      sessionStorage.setItem("fixture.initialized", "yes");
    }
  });
  let failFirst = true;
  let delaySecond = false;
  let unauthorizedSecond = false;
  let secondStarted;
  const secondLoadStarted = new Promise((resolve) => { secondStarted = resolve; });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Альфа" }];
    } else if (path === "/api/projects/alpha/chats") {
      data = [{ id: 1, title: "Первый чат" }, { id: 2, title: "Второй чат" }];
    } else if (path === "/api/chats/1/messages") {
      if (failFirst) {
        await route.fulfill({ status: 503, json: { error: "unavailable" } }).catch(() => {});
        return;
      }
      data = [{ id: 11, role: "agent", content: "Сохранённая история" }];
    } else if (path === "/api/chats/2/messages") {
      if (unauthorizedSecond) {
        await route.fulfill({ status: 401, json: { error: "expired" } }).catch(() => {});
        return;
      }
      if (delaySecond) {
        secondStarted();
        await delayedSecond;
        await route.fulfill({ status: 401, json: { error: "expired" } }).catch(() => {});
        return;
      }
      data = [{ id: 21, role: "agent", content: "История второго чата" }];
    }
    await route.fulfill({ status: 200, json: data }).catch(() => {});
  });

  await page.goto(url, { timeout: 15000 });
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await page.getByRole("alert").filter({ hasText: "Не удалось загрузить историю чата" }).waitFor();
  assert.equal(await page.getByText("Попробуй:", { exact: false }).count(), 0);
  const input = page.getByPlaceholder("Скажи агенту, что сделать...");
  await input.fill("Не теряй этот черновик");
  failFirst = false;
  await page.getByRole("button", { name: "Повторить загрузку истории" }).click();
  await page.getByText("Сохранённая история").waitFor();
  assert.equal(await input.inputValue(), "Не теряй этот черновик");
  assert.equal(await page.getByRole("alert").filter({ hasText: "Не удалось загрузить историю чата" }).count(), 0);

  await page.getByRole("button", { name: "Второй чат" }).click();
  await page.getByText("История второго чата").waitFor();
  failFirst = true;
  await page.getByRole("button", { name: "Первый чат" }).click();
  await page.getByRole("alert").filter({ hasText: "Не удалось загрузить историю чата" }).waitFor();
  assert.equal(await page.getByText("Сохранённая история").count(), 1);
  assert.equal(await input.inputValue(), "Не теряй этот черновик");
  failFirst = false;
  await page.getByRole("button", { name: "Повторить загрузку истории" }).click();
  await page.getByRole("alert").filter({ hasText: "Не удалось загрузить историю чата" }).waitFor({ state: "hidden" });

  delaySecond = true;
  await page.getByRole("button", { name: "Второй чат" }).click();
  await secondLoadStarted;
  await page.getByRole("button", { name: "Первый чат" }).click();
  await page.getByText("Сохранённая история").waitFor();
  releaseSecond();
  await delay(200);
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-token");
  assert.equal(await page.getByText("Сохранённая история").count(), 1);
  assert.equal(await input.inputValue(), "Не теряй этот черновик");
  delaySecond = false;
  unauthorizedSecond = true;
  await page.getByRole("button", { name: "Второй чат" }).click();
  await page.getByRole("button", { name: "Войти", exact: true }).waitFor();
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), null);
  console.log("PASS: chat history errors recover without losing drafts or exposing stale responses");
} finally {
  releaseSecond();
  if (browser) await browser.close();
  server.kill();
}
