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
  const page = await browser.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(15000);
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "fixture-a");
    localStorage.setItem("akhflow.project", "alpha");
    const original = window.fetch.bind(window);
    window.memberMode = "fail";
    window.memberRequests = [];
    window.fetch = (input, options = {}) => {
      const path = String(input);
      if (!path.endsWith("/members") || !options.signal || options.method) return original(input, options);
      const beta = path.includes("/beta/");
      const request = { signal: options.signal, path, token: options.headers.Authorization };
      window.memberRequests.push(request);
      const reply = (status, name) => {
        const members = [{ id: beta ? 2 : 1, display_name: name, email: "fixture@example.invalid", role: "editor" }];
        const payload = status === 200 ? members : { error: "unavailable" };
        return new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });
      };
      if (window.memberMode === "hold") {
        return new Promise((resolve) => { request.release = (status, name = "Old member") => resolve(reply(status, name)); });
      }
      if (window.memberMode === "fail") return Promise.resolve(reply(503, ""));
      return Promise.resolve(reply(200, beta ? "Beta member" : "Alpha member"));
    };
  });
  const current = { id: 1, title: "Current task", status: "todo", priority: "medium" };
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path.endsWith("/events")) {
      await route.fulfill({ status: 403, body: "" });
      return;
    }
    if (path.endsWith("/members") && request.method() === "PUT") {
      await route.fulfill({ json: { status: "ok" } });
      return;
    }
    let data = [];
    if (path === "/api/projects") data = [{ id: 1, slug: "alpha", name: "Alpha" }, { id: 2, slug: "beta", name: "Beta" }];
    if (path.endsWith("/tasks")) data = [current];
    if (path === "/api/tasks/1") data = { task: current, dependencies: [], blocks: [], comments: [], history: [] };
    if (path.endsWith("/role")) data = { role: "owner" };
    if (path.endsWith("/members")) data = [{ id: 1, display_name: "Alpha member", email: "fixture@example.invalid", role: "editor" }];
    await route.fulfill({ json: data });
  });
  await page.goto(url);
  const error = page.getByRole("alert").filter({ hasText: "Не удалось загрузить участников проекта" });
  const retry = page.getByRole("button", { name: "Повторить загрузку участников" });
  await error.waitFor();
  await page.getByRole("button", { name: "Current task", exact: true }).click();
  const assignee = page.getByRole("combobox", { name: "Исполнитель" });
  await assignee.waitFor();
  assert.equal(await assignee.isDisabled(), true, "assignee must not be changed before members are loaded");
  await error.waitFor();
  await page.evaluate(() => { window.memberMode = "hold"; });
  await retry.click();
  await page.waitForFunction(() => window.memberRequests.some((item) => item.release && !item.signal.aborted));
  assert.equal(await retry.isDisabled(), true, "retry must be disabled while loading");
  await page.evaluate(() => window.memberRequests.findLast((item) => item.release && !item.signal.aborted).release(200, "Alpha member"));
  await error.waitFor({ state: "detached" });
  await assignee.locator("option", { hasText: "Alpha member" }).waitFor({ state: "attached" });
  assert.equal(await assignee.isDisabled(), false);

  await page.getByRole("button", { name: "Закрыть карточку задачи" }).click();
  await page.getByRole("button", { name: "Участники и роли" }).click();
  await page.getByLabel("Email зарегистрированного пользователя").fill("new@example.invalid");
  await page.evaluate(() => { window.memberMode = "fail"; });
  await page.getByRole("button", { name: "Сохранить права" }).click();
  await error.waitFor();
  await page.getByRole("button", { name: "Current task", exact: true }).click();
  await assignee.locator("option", { hasText: "Alpha member" }).waitFor({ state: "attached" });
  assert.equal(await assignee.isDisabled(), false, "failed refresh must preserve the last member list");
  await page.evaluate(() => { window.memberMode = "hold"; });
  await retry.click();
  await page.waitForFunction(() => window.memberRequests.some((item) => item.release && !item.signal.aborted));
  await page.getByRole("button", { name: "Закрыть карточку задачи" }).click();
  await page.evaluate(() => { window.memberMode = "success"; });
  await page.locator("header select").first().selectOption("beta");
  await page.getByRole("button", { name: "Current task", exact: true }).click();
  await assignee.locator("option", { hasText: "Beta member" }).waitFor({ state: "attached" });
  await page.evaluate(() => window.memberRequests.findLast((item) => item.release).release(401));
  await delay(150);
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-a", "old project 401 must not invalidate the session");
  assert.equal(await assignee.locator("option", { hasText: "Beta member" }).count(), 1);
  assert.equal(await error.count(), 0);

  await page.getByRole("button", { name: "Закрыть карточку задачи" }).click();
  await page.evaluate(() => { window.memberMode = "fail"; });
  await page.locator("header select").first().selectOption("alpha");
  await error.waitFor();
  await page.evaluate(() => { window.memberMode = "hold"; });
  await retry.click();
  await page.waitForFunction(() => window.memberRequests.some((item) => item.release && !item.signal.aborted));
  await page.evaluate(() => {
    window.memberMode = "success";
    localStorage.setItem("akhflow.token", "fixture-b");
    window.dispatchEvent(new StorageEvent("storage", { key: "akhflow.token", storageArea: localStorage }));
  });
  await page.getByRole("button", { name: "Current task", exact: true }).click();
  await assignee.locator("option", { hasText: "Alpha member" }).waitFor({ state: "attached" });
  await page.evaluate(() => window.memberRequests.findLast((item) => item.release).release(401));
  await delay(150);
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-b", "old account 401 must not invalidate the new session");
  assert.equal(await error.count(), 0);
  assert.deepEqual(pageErrors, []);
  console.log("PASS: members loading, retry, refresh and workspace isolation");
} finally {
  if (browser) await browser.close();
  server.kill();
}
