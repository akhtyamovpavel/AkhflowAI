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
let tasksFail = true;

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
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "fixture-a");
    localStorage.setItem("akhflow.project", "alpha");
    const original = window.fetch.bind(window);
    window.epicMode = "fail";
    window.epicRequests = [];
    window.streams = [];
    window.createdEpic = false;
    window.fetch = (input, options = {}) => {
      const path = String(input);
      if (path.endsWith("/events")) {
        const stream = { signal: options.signal, path };
        window.streams.push(stream);
        const body = new ReadableStream({
          start(controller) {
            stream.controller = controller;
            options.signal.addEventListener("abort", () => {
              try {
                controller.error(new DOMException("Aborted", "AbortError"));
              } catch {
                // A closed stream needs no cleanup.
              }
            });
          },
        });
        return Promise.resolve(new Response(body, { headers: { "Content-Type": "text/event-stream" } }));
      }
      if (!path.endsWith("/epics") || options.method === "POST") return original(input, options);
      const beta = path.includes("/beta/");
      const reply = (status, title) => {
        const payload = status === 200 ? [{ id: beta ? 2 : 1, title, status: "open", description: "" }] : { error: "unavailable" };
        return new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });
      };
      const request = { signal: options.signal, path, token: options.headers.Authorization };
      window.epicRequests.push(request);
      if (window.epicMode === "hold") {
        return new Promise((resolve) => { request.release = (status, title = "Old epic") => resolve(reply(status, title)); });
      }
      if (window.epicMode === "fail") return Promise.resolve(reply(503, ""));
      const title = beta ? "Beta epic" : window.createdEpic ? "Created epic" : "Alpha epic";
      return Promise.resolve(reply(200, title));
    };
    window.importEvent = () => {
      const stream = window.streams.findLast((item) => !item.signal.aborted);
      const id = stream.path.includes("/beta/") ? 2 : 1;
      const event = JSON.stringify({ type: "tasks_imported", project_id: id });
      stream.controller.enqueue(new TextEncoder().encode(`data: ${event}\n\n`));
    };
  });
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/projects") {
      const projects = [{ id: 1, slug: "alpha", name: "Alpha" }, { id: 2, slug: "beta", name: "Beta" }];
      await route.fulfill({ json: projects });
      return;
    }
    if (path.endsWith("/epics") && route.request().method() === "POST") {
      await page.evaluate(() => { window.createdEpic = true; });
      await route.fulfill({ json: { id: 1, title: "Created epic" } });
      return;
    }
    if (path.endsWith("/tasks") && route.request().method() === "GET") {
      if (tasksFail) {
        await route.fulfill({ status: 503, json: { error: "unavailable" } });
      } else {
        await route.fulfill({ json: [] });
      }
      return;
    }
    if (path.endsWith("/role")) {
      await route.fulfill({ json: { role: "owner" } });
      return;
    }
    await route.fulfill({ json: [] });
  });
  await page.goto(url);
  const error = page.getByRole("alert").filter({ hasText: "Не удалось загрузить эпики" });
  const tasksError = page.getByRole("alert").filter({ hasText: "Не удалось загрузить задачи" });
  const retry = page.getByRole("button", { name: "Повторить загрузку эпиков" });
  const tasksRetry = page.getByRole("button", { name: "Повторить загрузку задач" });
  const epicSelect = page.locator("header select").nth(1);
  await error.waitFor();
  await tasksError.waitFor();
  await page.waitForFunction(() => window.streams.some((stream) => !stream.signal.aborted));
  tasksFail = false;
  await tasksRetry.click();
  await tasksError.waitFor({ state: "detached" });
  assert.equal(await error.count(), 1, "task retry must not clear the epic error");
  await page.evaluate(() => { window.epicMode = "hold"; });
  await retry.click();
  await page.waitForFunction(() => window.epicRequests.some((request) => request.release && !request.signal.aborted));
  assert.equal(await retry.isDisabled(), true, "retry must be disabled while loading");
  await page.evaluate(() => window.epicRequests.findLast((request) => request.release && !request.signal.aborted).release(503));
  await page.waitForFunction(() => {
    const button = [...document.querySelectorAll("button")].find((item) => item.textContent?.includes("Повторить загрузку эпиков"));
    return button && !button.disabled;
  });
  await page.evaluate(() => { window.epicMode = "success"; });
  await retry.click();
  await epicSelect.locator("option", { hasText: "Alpha epic" }).waitFor({ state: "attached" });
  assert.equal(await error.count(), 0, "successful retry must clear error");

  await page.evaluate(() => { window.epicMode = "fail"; window.importEvent(); });
  await error.waitFor();
  assert.equal(await epicSelect.locator("option", { hasText: "Alpha epic" }).count(), 1, "failed refresh must preserve epics");
  await page.evaluate(() => { window.epicMode = "hold"; });
  await retry.click();
  await page.waitForFunction(() => window.epicRequests.some((request) => request.release && !request.signal.aborted));
  await page.evaluate(() => { window.epicMode = "success"; window.importEvent(); });
  await page.waitForFunction(() => !document.querySelector('[role="alert"]'));
  await page.evaluate(() => window.epicRequests.findLast((request) => request.release).release(503));
  await delay(150);
  assert.equal(await error.count(), 0, "older failure must not hide successful event refresh");
  assert.equal(await epicSelect.locator("option", { hasText: "Alpha epic" }).count(), 1);

  await page.getByRole("button", { name: "Эпик", exact: true }).click();
  await page.getByPlaceholder("Название эпика").fill("Created epic");
  await page.getByPlaceholder("Название эпика").press("Enter");
  await epicSelect.locator("option", { hasText: "Created epic" }).waitFor({ state: "attached" });

  await page.evaluate(() => { window.epicMode = "hold"; window.importEvent(); });
  await page.waitForFunction(() => window.epicRequests.some((request) => request.release && !request.signal.aborted));
  await page.evaluate(() => { window.epicMode = "success"; });
  await page.locator("header select").first().selectOption("beta");
  await epicSelect.locator("option", { hasText: "Beta epic" }).waitFor({ state: "attached" });
  await page.evaluate(() => window.epicRequests.findLast((request) => request.release).release(401));
  await delay(150);
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-a", "old project must not invalidate session");
  assert.equal(await epicSelect.locator("option", { hasText: "Beta epic" }).count(), 1);
  assert.equal(await error.count(), 0);

  await page.evaluate(() => { window.epicMode = "hold"; window.importEvent(); });
  await page.waitForFunction(() => window.epicRequests.some((request) => request.release && !request.signal.aborted));
  await page.evaluate(() => {
    window.epicMode = "success";
    localStorage.setItem("akhflow.token", "fixture-b");
    window.dispatchEvent(new StorageEvent("storage", { key: "akhflow.token", storageArea: localStorage }));
  });
  await epicSelect.locator("option", { hasText: "Beta epic" }).waitFor({ state: "attached" });
  await page.evaluate(() => window.epicRequests.findLast((request) => request.release).release(401));
  await delay(150);
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-b", "old account must not invalidate new session");
  assert.equal(await error.count(), 0);
  console.log("PASS: epic loading, retry, event refresh, creation and workspace isolation");
} finally {
  if (browser) await browser.close();
  server.kill();
}
