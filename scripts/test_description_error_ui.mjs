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
const task = { id: 1, title: "Задача А", description: "Исходное", status: "backlog", priority: "medium" };
let mode = "http-error";
let releaseSave;
let saveStarted;
let browser;
const pageErrors = [];

try {
  let ready = false;
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      ready = (await fetch(url)).ok;
    } catch {
      await delay(100);
    }
    if (ready) {
      break;
    }
  }
  assert.ok(ready, "Vite did not start");
  browser = await playwright.chromium.launch({ executablePath: "/usr/bin/google-chrome", headless: true });
  const page = await browser.newPage();
  page.on("pageerror", (error) => pageErrors.push(error.message));
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "fixture-token");
    localStorage.setItem("akhflow.project", "alpha");
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Альфа" }];
    } else if (path === "/api/projects/alpha/tasks") {
      data = [task];
    } else if (path === "/api/tasks/1" && request.method() === "PATCH") {
      if (mode === "http-error") {
        await route.fulfill({ status: 500, json: { error: "failure" } });
        return;
      }
      if (mode === "network-error") {
        await route.abort();
        return;
      }
      if (mode === "delayed") {
        saveStarted();
        await new Promise((resolve) => { releaseSave = resolve; });
      }
      Object.assign(task, request.postDataJSON());
    } else if (path === "/api/tasks/1") {
      if (mode === "refresh-error") {
        await route.fulfill({ status: 500, json: { error: "failure" } });
        return;
      }
      data = { task, dependencies: [], comments: [], blocks: [], history: [] };
    }
    await route.fulfill({ status: 200, json: data }).catch(() => {});
  });
  await page.goto(url);
  await page.getByRole("button", { name: "Задача А", exact: true }).click();
  const description = page.getByPlaceholder("Описание задачи...");
  const save = page.getByRole("button", { name: "Сохранить", exact: true });
  await description.fill("Черновик");
  await save.click();
  await page.getByRole("alert").getByText("Не удалось сохранить описание. Попробуй ещё раз.").waitFor();
  assert.equal(await description.inputValue(), "Черновик");
  assert.equal(task.description, "Исходное");
  mode = "network-error";
  await save.click();
  await page.getByRole("alert").getByText("Не удалось сохранить описание. Попробуй ещё раз.").waitFor();
  assert.equal(await description.inputValue(), "Черновик");
  mode = "delayed";
  const started = new Promise((resolve) => { saveStarted = resolve; });
  await save.click();
  await started;
  await description.fill("Новый текст");
  releaseSave();
  await save.waitFor({ state: "visible" });
  await page.waitForFunction(() => [...document.querySelectorAll("button")].some((button) => {
    return button.textContent === "Сохранить" && !button.disabled;
  }));
  assert.equal(task.description, "Черновик");
  assert.equal(await description.inputValue(), "Новый текст");
  assert.equal(await page.getByRole("alert").count(), 0);
  mode = "success";
  await save.click();
  await page.waitForFunction(() => [...document.querySelectorAll("button")].some((button) => {
    return button.textContent === "Сохранить" && !button.disabled;
  }));
  assert.equal(task.description, "Новый текст");
  await description.fill("Сохранено до ошибки обновления");
  mode = "refresh-error";
  await save.click();
  await page.getByRole("alert").getByText("Описание сохранено, но обновить карточку не удалось.").waitFor();
  assert.equal(task.description, "Сохранено до ошибки обновления");
  assert.equal(await description.inputValue(), "Сохранено до ошибки обновления");
  assert.deepEqual(pageErrors, []);
  console.log("PASS: description errors preserve draft, retry succeeds and refresh failure is distinct");
} finally {
  if (releaseSave) {
    releaseSave();
  }
  if (browser) {
    await browser.close();
  }
  server.kill();
}
