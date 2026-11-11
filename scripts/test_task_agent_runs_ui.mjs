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
  let role = "owner";
  let rejection = 0;
  let conflictMatches = false;
  let malformed = false;
  let hideRuns = true;
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let data = [];
    if (path === "/api/projects") data = [{ id: 1, slug: "alpha", name: "Альфа" }];
    if (path === "/api/projects/alpha/tasks") data = [task];
    if (path === "/api/projects/alpha/role") data = { role };
    if (path === "/api/tasks/1") data = { task, dependencies: [], comments: [], blocks: [], history: [] };
    if (path === "/api/projects/alpha/chats") data = [{ id: 9, title: "Тестовая задача" }];
    if (path === "/api/tasks/1/runs" && request.method() === "POST") {
      posts++;
      if (rejection) {
        if (rejection === 409) {
          const message = conflictMatches ? request.postDataJSON().message : "Другой запрос";
          runs = [{ id: 5, chat_id: 9, task_id: 1, actor_id: 1, request_key: request.headers()["idempotency-key"], message, status: "failed", cancel_requested: false, step: { iteration: 1, max_iterations: 8 }, steps: [], result: null }];
        }
        const error = rejection === 503 ? "run queue full" : "fixture";
        await route.fulfill({ status: rejection, json: { error } });
        return;
      }
      if (malformed) {
        await route.fulfill({ status: 202, json: { status: "queued" } });
        return;
      }
      runs = [{ id: 4, chat_id: 9, task_id: 1, actor_id: 1, request_key: request.headers()["idempotency-key"], message: "Проверь", status: "running", cancel_requested: false, step: { iteration: 1, max_iterations: 8 }, steps: [], result: null }];
      await route.abort("connectionreset");
      return;
    }
    if (path === "/api/tasks/1/runs") data = hideRuns ? [] : runs;
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
  await page.getByRole("button", { name: "Повторить запрос" }).waitFor();
  const originalPending = await page.evaluate(() => localStorage.getItem("akhflow.task-run.1.1"));
  rejection = 403;
  await page.getByRole("button", { name: "Повторить запрос" }).click();
  await page.getByText("Не удалось подтвердить запуск. Проверь состояние или повтори запрос.").waitFor();
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.task-run.1.1")), originalPending, "retry rejection must preserve uncertain key");
  rejection = 0;
  hideRuns = false;
  await page.getByText("Агент работает").waitFor();
  assert.equal(posts, 2, "reload must not automatically submit another run");
  await page.getByRole("button", { name: "Остановить" }).click();
  await page.getByText("Результат действия не подтверждён").waitFor();
  runs[0].status = "succeeded";
  runs[0].result = { reply: "Готовый результат" };
  await page.reload();
  await page.getByRole("button", { name: "Тестовая задача", exact: true }).click();
  await page.getByText("Готовый результат").waitFor();
  for (const status of [403, 503, 409]) {
    rejection = status;
    await page.getByLabel("Запрос агенту").fill(`Ошибка ${status}`);
    await page.getByRole("button", { name: "Запустить агента" }).click();
    await page.getByText("Запрос не принят. Проверь доступ и повтори попытку.").waitFor();
    assert.equal(await page.getByLabel("Запрос агенту").isEnabled(), true, `HTTP ${status} must release the form`);
    await delay(3200);
    assert.equal(await page.getByText("Запрос не принят. Проверь доступ и повтори попытку.").count(), 1, "polling must retain send errors");
  }
  rejection = 409;
  conflictMatches = true;
  await page.getByLabel("Запрос агенту").fill("Совпадающий запрос");
  await page.getByRole("button", { name: "Запустить агента" }).click();
  await page.waitForFunction(() => !localStorage.getItem("akhflow.task-run.1.1"));
  assert.equal(await page.getByLabel("Запрос агенту").inputValue(), "", "matching key must recover the accepted request");
  rejection = 0;
  malformed = true;
  await page.getByLabel("Запрос агенту").fill("Неясный ответ");
  await page.getByRole("button", { name: "Запустить агента" }).click();
  await page.getByText("Не удалось подтвердить запуск. Проверь состояние или повтори запрос.").waitFor();
  assert.notEqual(await page.evaluate(() => localStorage.getItem("akhflow.task-run.1.1")), null, "malformed response must preserve key");
  await page.evaluate(() => localStorage.removeItem("akhflow.task-run.1.1"));
  malformed = false;
  rejection = 0;
  runs[0].status = "running";
  runs[0].actor_id = 2;
  role = "viewer";
  await page.reload();
  await page.getByRole("button", { name: "Тестовая задача", exact: true }).click();
  await page.getByText("Агент работает").waitFor();
  assert.equal(await page.getByRole("button", { name: "Запустить агента" }).isDisabled(), true, "viewer cannot start");
  assert.equal(await page.getByRole("button", { name: "Остановить" }).count(), 0, "viewer cannot cancel");
  role = "editor";
  await page.reload();
  await page.getByRole("button", { name: "Тестовая задача", exact: true }).click();
  await page.getByText("Агент работает").waitFor();
  assert.equal(await page.getByRole("button", { name: "Остановить" }).count(), 0, "editor cannot cancel another actor's run");
  await page.getByRole("button", { name: "Открыть чат" }).click();
  await page.getByRole("button", { name: "Тестовая задача", exact: true }).waitFor();
  console.log("PASS: task run recovery, cancellation, result and chat link");
} finally {
  if (browser) await browser.close();
  server.kill("SIGTERM");
}
