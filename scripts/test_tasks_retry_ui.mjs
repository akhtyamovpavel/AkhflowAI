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
  for (let attempt = 0; attempt < 150; attempt++) {
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
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/role")) {
      await route.fulfill({ json: { role: "owner" } });
      return;
    }
    await route.fulfill({ json: [] });
  });
  const page = await context.newPage();
  page.setDefaultTimeout(7000);
  await page.addInitScript(() => {
    if (!sessionStorage.getItem("test-started")) {
      localStorage.setItem("akhflow.token", "fixture-a");
      sessionStorage.setItem("test-started", "1");
    }
    const original = window.fetch.bind(window);
    const projects = [{ id: 1, slug: "alpha", name: "Alpha" }, { id: 2, slug: "beta", name: "Beta" }];
    window.taskMode = "fail";
    window.pendingTasks = [];
    window.taskCalls = 0;
    window.eventStreams = {};
    window.eventConnections = 0;
    const json = (body, status = 200) => new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    });
    window.emitTaskEvent = (project) => {
      const event = { project_id: project === "alpha" ? 1 : 2, type: "task_updated", title: "Changed" };
      window.eventStreams[project].enqueue(new TextEncoder().encode(`data: ${JSON.stringify(event)}\n\n`));
    };
    window.fetch = (input, options = {}) => {
      const path = new URL(String(input), location.origin).pathname;
      if (path === "/api/projects") return Promise.resolve(json(projects));
      const stream = path.match(/^\/api\/projects\/(alpha|beta)\/events$/);
      if (stream) {
        window.eventConnections++;
        const body = new ReadableStream({ start(controller) { window.eventStreams[stream[1]] = controller; } });
        return Promise.resolve(new Response(body, { headers: { "Content-Type": "text/event-stream" } }));
      }
      const tasks = path.match(/^\/api\/projects\/(alpha|beta)\/tasks$/);
      if (tasks && (!options.method || options.method === "GET")) {
        window.taskCalls++;
        const name = tasks[1] === "alpha" ? "Alpha task" : "Beta task";
        const reply = (status) => json(status === 200 ? [{ id: tasks[1] === "alpha" ? 1 : 2, title: name, status: "backlog", priority: "medium" }] : { error: "unavailable" }, status);
        if (window.taskMode === "hold") {
          return new Promise((resolve) => window.pendingTasks.push({
            project: tasks[1],
            signal: options.signal,
            release: (status) => resolve(reply(status)),
          }));
        }
        if (window.taskMode === "fail") return Promise.resolve(reply(503));
        if (window.taskMode === "unauthorized") return Promise.resolve(reply(401));
        return Promise.resolve(reply(200));
      }
      return original(input, options);
    };
  });
  await page.goto(url, { timeout: 30000 });
  const error = page.getByRole("alert").filter({ hasText: "Не удалось загрузить задачи" });
  const retry = page.getByRole("button", { name: "Повторить загрузку задач" });
  const input = page.getByPlaceholder("Новая задача...");
  await error.waitFor();
  await page.waitForFunction(() => Boolean(window.eventStreams.alpha));
  await input.fill("Unsent draft");
  const count = await page.evaluate(() => window.taskCalls);
  await page.evaluate(() => { window.taskMode = "success"; });
  await delay(500);
  assert.equal(await page.evaluate(() => window.taskCalls), count, "an open SSE stream must not be mistaken for a retry");
  await retry.click();
  await page.getByText("Alpha task", { exact: true }).first().waitFor();
  assert.equal(await error.count(), 0);
  assert.equal(await input.inputValue(), "Unsent draft");

  await page.evaluate(() => {
    window.taskMode = "fail";
    window.emitTaskEvent("alpha");
  });
  await error.waitFor();
  await page.getByText("Alpha task", { exact: true }).first().waitFor();
  assert.equal(await input.inputValue(), "Unsent draft", "failed refresh must keep the draft");

  await page.evaluate(() => { window.taskMode = "hold"; });
  await retry.click();
  await page.waitForFunction(() => window.pendingTasks.length === 1);
  assert.equal(await retry.isDisabled(), true);
  await page.evaluate(() => window.emitTaskEvent("alpha"));
  await page.waitForFunction(() => window.pendingTasks.length === 2);
  await page.evaluate(() => window.pendingTasks[1].release(200));
  await error.waitFor({ state: "detached" });
  await page.evaluate(() => window.pendingTasks[0].release(503));
  await delay(100);
  assert.equal(await error.count(), 0, "an older error must not replace a newer success");
  assert.equal(await input.inputValue(), "Unsent draft");

  await page.evaluate(() => {
    window.pendingTasks = [];
    window.emitTaskEvent("alpha");
  });
  await page.waitForFunction(() => window.pendingTasks.length === 1);
  await page.evaluate(() => { window.taskMode = "success"; });
  await page.locator("header select").first().selectOption("beta");
  await page.getByText("Beta task", { exact: true }).first().waitFor();
  await page.evaluate(() => window.pendingTasks[0].release(401));
  await delay(100);
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-a", "a stale 401 must not log out the account");
  assert.equal(await error.count(), 0);

  await page.evaluate(() => {
    window.pendingTasks = [];
    window.taskMode = "hold";
    window.emitTaskEvent("beta");
  });
  await page.waitForFunction(() => window.pendingTasks.length === 1);
  const connections = await page.evaluate(() => window.eventConnections);
  await page.evaluate(() => {
    window.taskMode = "success";
    localStorage.setItem("akhflow.token", "fixture-b");
    window.dispatchEvent(new StorageEvent("storage", { key: "akhflow.token", storageArea: localStorage }));
  });
  await page.waitForFunction((previous) => window.eventConnections > previous, connections);
  await page.evaluate(() => window.pendingTasks[0].release(401));
  await delay(100);
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-b", "a previous account response must not log out the current account");
  assert.equal(await error.count(), 0);
  await page.evaluate(() => {
    window.taskMode = "unauthorized";
    window.emitTaskEvent("beta");
  });
  await page.waitForFunction(() => localStorage.getItem("akhflow.token") === null);
  console.log("PASS: task loading failure, retry, SSE and stale responses");
} finally {
  if (browser) await browser.close();
  server.kill();
}
