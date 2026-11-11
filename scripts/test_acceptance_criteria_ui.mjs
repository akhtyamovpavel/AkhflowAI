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
let release;
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
  const task = { id: 1, title: "Задача", description: "Описание", acceptance_criteria: "Исходное", status: "todo", priority: "medium" };
  let fail = true;
  let delayed = false;
  let started;
  let received = new Promise((resolve) => { started = resolve; });
  let wait = new Promise((resolve) => { release = resolve; });
  let priorityChanged;
  const prioritySaved = new Promise((resolve) => { priorityChanged = resolve; });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let data = [];
    if (path === "/api/projects") data = [{ id: 1, slug: "alpha", name: "Альфа" }];
    if (path === "/api/projects/alpha/tasks") data = [task];
    if (path === "/api/projects/alpha/role") data = { role: "owner" };
    if (path === "/api/tasks/1" && request.method() === "PATCH") {
      const patch = request.postDataJSON();
      if (Object.hasOwn(patch, "acceptance_criteria")) {
        if (fail) { await route.fulfill({ status: 500, json: { error: "fixture" } }); return; }
        if (delayed) { started(); await wait; }
      }
      Object.assign(task, patch);
      if (Object.hasOwn(patch, "priority")) priorityChanged();
    } else if (path === "/api/tasks/1") data = { task, dependencies: [], comments: [], blocks: [], history: [] };
    await route.fulfill({ status: 200, json: data }).catch(() => {});
  });
  await page.goto(url);
  await page.getByRole("button", { name: "Задача", exact: true }).click();
  const criteria = page.getByLabel("Критерии приёмки");
  await criteria.fill("Черновик");
  await page.getByRole("button", { name: "Сохранить критерии" }).click();
  await page.getByText("Не удалось сохранить критерии. Попробуй ещё раз.").waitFor();
  assert.equal(await criteria.inputValue(), "Черновик");
  await page.getByLabel("Приоритет").selectOption("high");
  await prioritySaved;
  await delay(100);
  assert.equal(await criteria.inputValue(), "Черновик", "refresh discarded criteria draft");
  fail = false;
  delayed = true;
  await page.getByRole("button", { name: "Сохранить критерии" }).click();
  await received;
  assert.equal(await criteria.isDisabled(), true, "editing must be disabled while saving");
  release();
  await page.waitForFunction(() => document.querySelector('#criteria-1') && !document.querySelector('#criteria-1').disabled);
  assert.equal(task.acceptance_criteria, "Черновик");
  assert.equal(await criteria.inputValue(), "Черновик");
  await criteria.fill("я".repeat(8193));
  await page.getByRole("button", { name: "Сохранить критерии" }).click();
  await page.getByText("Критерии должны занимать не больше 16 КиБ и не содержать нулевой символ.").waitFor();
  assert.equal(task.acceptance_criteria, "Черновик");
  console.log("PASS: criteria draft, save failure, refresh, byte limit and in-flight save");
} finally {
  if (release) release();
  if (browser) await browser.close();
  server.kill("SIGTERM");
}
