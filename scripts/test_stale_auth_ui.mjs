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
  const context = await browser.newContext();
  await context.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const authorization = request.headers().authorization ?? "";
    if (path === "/api/auth/login") {
      const email = request.postDataJSON().email;
      const account = email.split("@")[0];
      await route.fulfill({ json: { token: `fixture-${account}` } });
      return;
    }
    if (path.endsWith("/events")) {
      await route.fulfill({ status: 403, body: "" });
      return;
    }
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Alpha" }, { id: 2, slug: "beta", name: "Beta" }];
    }
    if (/^\/api\/projects\/(alpha|beta)\/tasks$/.test(path)) {
      data = [{ id: 1, title: `Task ${authorization}`, status: "todo", priority: "medium" }];
    }
    if (path.endsWith("/role")) data = { role: "owner" };
    await route.fulfill({ json: data });
  });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  await page.addInitScript(() => {
    const count = Number(sessionStorage.getItem("test-navigation-count") || 0) + 1;
    sessionStorage.setItem("test-navigation-count", String(count));
    const original = window.fetch.bind(window);
    window.pendingOld = [];
    window.pendingUsage = [];
    window.pendingChats = [];
    window.fetch = (input, options = {}) => {
      const path = String(input);
      const token = options.headers?.Authorization;
      const unauthorized = () => new Response('{"error":"unauthorized"}', { status: 401 });
      if (path.endsWith("/api/projects") && token === "Bearer fixture-account-a") {
        return new Promise((resolve) => window.pendingOld.push(() => resolve(unauthorized())));
      }
      if (path.includes("/agent/usage") && window.usageMode === "hold") {
        return new Promise((resolve) => window.pendingUsage.push({ signal: options.signal, release: () => resolve(unauthorized()) }));
      }
      if (path.includes("/agent/usage") && window.usageMode === "invalid") {
        return Promise.resolve(unauthorized());
      }
      if (path.includes("/agent/usage") && window.usageMode === "unavailable") {
        return Promise.resolve(new Response('{"error":"authentication temporarily unavailable"}', { status: 503 }));
      }
      if (path.endsWith("/beta/chats") && window.holdChats && token === "Bearer fixture-account-b") {
        return new Promise((resolve) => window.pendingChats.push(() => resolve(unauthorized())));
      }
      return original(input, options);
    };
  });
  const login = async (target, account) => {
    await target.getByPlaceholder("Email").fill(`${account}@example.invalid`);
    await target.getByPlaceholder("Пароль").fill("fixture-password");
    await target.getByRole("button", { name: "Войти", exact: true }).click();
  };
  await page.goto(url);
  await login(page, "account-a");
  await page.waitForFunction(() => window.pendingOld.length > 0);
  await page.getByRole("button", { name: "Выйти" }).click();
  await login(page, "account-b");
  await page.getByText("Task Bearer fixture-account-b", { exact: true }).first().waitFor();
  await page.evaluate(() => window.pendingOld.forEach((release) => release()));
  await delay(500);
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-account-b", "old 401 removed the new account");
  assert.equal(await page.evaluate(() => sessionStorage.getItem("test-navigation-count")), "1", "old 401 reloaded the page");

  await page.evaluate(() => { window.usageMode = "hold"; });
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await page.waitForFunction(() => window.pendingUsage.length > 0);
  await page.getByRole("button", { name: "Канбан", exact: true }).click();
  await page.waitForFunction(() => window.pendingUsage.every((request) => request.signal.aborted));
  await page.evaluate(() => window.pendingUsage.forEach((request) => request.release()));
  await delay(500);
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-account-b", "cancelled request logged out the account");
  assert.equal(await page.evaluate(() => sessionStorage.getItem("test-navigation-count")), "1", "cancelled request reloaded the page");

  const second = await context.newPage();
  second.setDefaultTimeout(5000);
  await second.goto(url);
  await second.getByText("Task Bearer fixture-account-b", { exact: true }).first().waitFor();
  await page.evaluate(() => { window.holdChats = true; });
  await page.locator("header select").first().selectOption("beta");
  await page.waitForFunction(() => window.pendingChats.length > 0);
  await second.getByRole("button", { name: "Выйти" }).click();
  await login(second, "account-c");
  await second.getByText("Task Bearer fixture-account-c", { exact: true }).first().waitFor();
  await page.getByText("Task Bearer fixture-account-c", { exact: true }).first().waitFor();
  await page.evaluate(() => window.pendingChats.forEach((release) => release()));
  await delay(500);
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-account-c", "old request in another tab logged out the new account");
  await second.getByText("Task Bearer fixture-account-c", { exact: true }).first().waitFor();

  await page.evaluate(() => { window.usageMode = "unavailable"; });
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await page.getByText("Не удалось загрузить метрики агента.").waitFor();
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-account-c", "temporary failure removed the token");
  assert.equal(await page.evaluate(() => sessionStorage.getItem("test-navigation-count")), "1", "temporary failure reloaded the page");
  await page.getByRole("button", { name: "Канбан", exact: true }).click();

  await page.evaluate(() => { window.usageMode = "invalid"; });
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await page.getByPlaceholder("Email").waitFor();
  await second.getByPlaceholder("Email").waitFor();
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), null, "current 401 must log out");
  console.log("PASS: stale and cancelled 401, cross-tab changes, temporary failure, current 401");
} finally {
  if (browser) await browser.close();
  server.kill();
}
