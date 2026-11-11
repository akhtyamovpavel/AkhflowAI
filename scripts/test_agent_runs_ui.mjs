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
    try {
      if ((await fetch(url)).ok) break;
    } catch {
      await delay(100);
    }
  }
  browser = await playwright.chromium.launch({ executablePath: "/usr/bin/google-chrome", headless: true });
  const page = await browser.newPage();
  const token = `fixture.${Buffer.from('{"sub":"1"}').toString("base64url")}.fixture`;
  await page.addInitScript((value) => {
    localStorage.setItem("akhflow.token", value);
    localStorage.setItem("akhflow.project", "alpha");
  }, token);
  let runs = [];
  let posts = 0;
  let key = "";
  let historyLoads = 0;
  let invalidList = null;
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let data = [];
    if (path === "/api/projects") data = [{ id: 1, slug: "alpha", name: "Alpha" }];
    if (path === "/api/projects/alpha/chats") data = [{ id: 1, title: "Первый" }, { id: 2, title: "Второй" }];
    if (path === "/api/chats/1/runs" && request.method() === "POST") {
      posts++;
      key = request.headers()["idempotency-key"];
      runs = [{ id: 4, chat_id: 1, actor_id: 1, request_key: key, message: "Проверка", status: "running", cancel_requested: false, step: { iteration: 1, max_iterations: 8 }, steps: [] }];
      await route.abort("connectionreset");
      return;
    }
    if (path === "/api/chats/1/messages") historyLoads++;
    if (path === "/api/chats/1/runs") data = invalidList ?? runs;
    if (path === "/api/runs/4/cancel") {
      runs[0].status = "canceled";
      await route.fulfill({ status: 202, json: runs[0] });
      return;
    }
    await route.fulfill({ status: 200, json: data });
  });
  await page.goto(url);
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  const input = page.getByPlaceholder("Скажи агенту, что сделать...");
  await input.fill("Проверка");
  await input.press("Enter");
  await page.getByText("Результат отправки неизвестен.", { exact: false }).waitFor();
  await page.reload();
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await page.getByRole("button", { name: "Проверить запуск" }).click();
  await page.getByText("Агент работает", { exact: false }).waitFor();
  assert.equal(posts, 1);
  await page.getByRole("button", { name: "Второй" }).click();
  await page.getByRole("button", { name: "Первый" }).click();
  await page.getByRole("button", { name: "Остановить" }).click();
  await page.getByText("Отменено", { exact: false }).waitFor();
  runs[0].status = "interrupted";
  runs[0].steps = [{ sequence: 1, status: "interrupted", summary: "Действие выполняется" }];
  await page.reload();
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await page.getByText("Результат действия не подтверждён").waitFor();
  assert.equal(await page.getByText("Действие выполняется").count(), 0);
  runs[0].status = "step_limit";
  const beforeHistory = historyLoads;
  await page.reload();
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await page.getByText("Достигнут предел шагов", { exact: false }).waitFor();
  assert.equal(await page.getByText("Выполнено", { exact: true }).count(), 0);
  for (let attempt = 0; historyLoads < beforeHistory + 2 && attempt < 100; attempt++) await delay(20);
  assert.ok(historyLoads >= beforeHistory + 2, "step limit must refresh history on first poll");
  for (const invalid of [[null], [{ id: 1, steps: {} }]]) {
    invalidList = invalid;
    await page.reload();
    await page.getByRole("button", { name: "Агент", exact: true }).click();
    await page.getByText("Не удалось обновить состояние агента", { exact: false }).waitFor();
    assert.equal(await page.getByPlaceholder("Скажи агенту, что сделать...").count(), 1);
  }
  console.log("PASS: recovery, switching, cancellation and step limit");
} finally {
  if (browser) await browser.close();
  server.kill("SIGTERM");
}
