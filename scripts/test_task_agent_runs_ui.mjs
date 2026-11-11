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
try {
  for (let attempt = 0; attempt < 100; attempt++) {
    try { if ((await fetch(url)).ok) break; } catch { await delay(100); }
  }
  browser = await playwright.chromium.launch({ executablePath: "/usr/bin/google-chrome", headless: true });
  const page = await browser.newPage();
  const token = `fixture.${Buffer.from('{"sub":"1"}').toString("base64url")}.fixture`;
  await page.addInitScript((value) => {
    localStorage.setItem("akhflow.token", value);
    localStorage.setItem("akhflow.project", "alpha");
  }, token);
  const task = { id: 1, title: "Тестовая задача", description: "Описание", status: "todo", priority: "medium" };
  let runs = [];
  let posts = 0;
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let data = [];
    if (path === "/api/projects") data = [{ id: 1, slug: "alpha", name: "Альфа" }];
    if (path === "/api/projects/alpha/tasks") data = [task];
    if (path === "/api/tasks/1") data = { task, dependencies: [], comments: [], blocks: [], history: [] };
    if (path === "/api/projects/alpha/chats") data = [{ id: 9, title: "Тестовая задача" }];
    if (path === "/api/tasks/1/runs" && request.method() === "POST") {
      posts++;
      runs = [{ id: 4, chat_id: 9, task_id: 1, actor_id: 1, request_key: request.headers()["idempotency-key"], message: "Проверь", status: "running", cancel_requested: false, step: { iteration: 1, max_iterations: 8 }, steps: [], result: null }];
      await route.abort("connectionreset");
      return;
    }
    if (path === "/api/tasks/1/runs") data = runs;
    if (path === "/api/runs/4/cancel") {
      runs[0].status = "interrupted";
      runs[0].steps = [{ sequence: 1, status: "interrupted", summary: "Результат действия не подтверждён" }];
      await route.fulfill({ status: 202, json: runs[0] });
      return;
    }
    await route.fulfill({ status: 200, json: data }).catch(() => {});
  });
  await page.goto(url);
  await page.getByRole("button", { name: "Тестовая задача", exact: true }).click();
  await page.getByLabel("Запрос агенту").fill("Проверь");
  await page.getByRole("button", { name: "Запустить агента" }).click();
  await page.reload();
  await page.getByRole("button", { name: "Тестовая задача", exact: true }).click();
  await page.getByText("Агент работает").waitFor();
  assert.equal(posts, 1, "reload must not submit another run");
  await page.getByRole("button", { name: "Остановить" }).click();
  await page.getByText("Результат действия не подтверждён").waitFor();
  runs[0].status = "succeeded";
  runs[0].result = { reply: "Готовый результат" };
  await page.reload();
  await page.getByRole("button", { name: "Тестовая задача", exact: true }).click();
  await page.getByText("Готовый результат").waitFor();
  await page.getByRole("button", { name: "Открыть чат" }).click();
  await page.getByRole("button", { name: "Тестовая задача", exact: true }).waitFor();
  console.log("PASS: task run recovery, cancellation, result and chat link");
} finally {
  if (browser) await browser.close();
  server.kill("SIGTERM");
}
