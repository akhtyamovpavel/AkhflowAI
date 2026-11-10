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
const first = { id: 1, title: "Задача А", description: "Первое описание", status: "backlog", priority: "medium" };
const second = { id: 2, title: "Задача Б", description: "Второе описание", status: "todo", priority: "medium" };
let releaseSave;
const saveWait = new Promise((resolve) => { releaseSave = resolve; });
let saveStarted;
const saveReceived = new Promise((resolve) => { saveStarted = resolve; });
let priorityReloaded;
const priorityReload = new Promise((resolve) => { priorityReloaded = resolve; });
let priorityChanged = false;
let browser;

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
      data = [first, second];
    } else if (path === "/api/tasks/1" && request.method() === "PATCH") {
      const patch = request.postDataJSON();
      if (Object.hasOwn(patch, "description")) {
        saveStarted();
        await saveWait;
      }
      Object.assign(first, patch);
      if (Object.hasOwn(patch, "priority")) {
        priorityChanged = true;
      }
    } else if (path === "/api/tasks/1") {
      data = { task: first, dependencies: [second], comments: [], blocks: [], history: [] };
    } else if (path === "/api/tasks/2") {
      data = { task: second, dependencies: [first], comments: [], blocks: [], history: [] };
    }
    await route.fulfill({ status: 200, json: data }).catch(() => {});
    if (path === "/api/tasks/1" && request.method() === "GET" && priorityChanged) {
      priorityChanged = false;
      priorityReloaded();
    }
  });

  await page.goto(url);
  await page.getByRole("button", { name: "Задача А", exact: true }).click();
  const description = page.getByPlaceholder("Описание задачи...");
  await description.waitFor();
  assert.equal(await description.inputValue(), "Первое описание");
  await description.fill("Черновик");
  await page.getByLabel("Приоритет").selectOption("high");
  await priorityReload;
  await page.waitForFunction(() => [...document.querySelectorAll("label")].some((label) => {
    return label.textContent.includes("Приоритет") && label.querySelector("select")?.value === "high";
  }));
  assert.equal(await description.inputValue(), "Черновик", "background refresh discarded draft");
  await page.getByRole("button", { name: "Сохранить", exact: true }).click();
  await saveReceived;
  await description.fill("Черновик после отправки");
  releaseSave();
  await page.waitForFunction(() => [...document.querySelectorAll("button")].some((button) => {
    return button.textContent === "Сохранить" && !button.disabled;
  }));
  assert.equal(first.description, "Черновик");
  assert.equal(await description.inputValue(), "Черновик после отправки", "save discarded later typing");
  await page.getByRole("button", { name: "Сохранить", exact: true }).click();
  await page.waitForFunction(() => [...document.querySelectorAll("button")].some((button) => {
    return button.textContent === "Сохранить" && !button.disabled;
  }));
  assert.equal(first.description, "Черновик после отправки");
  first.description = "Обновлено на сервере";
  await page.getByLabel("Приоритет").selectOption("medium");
  await page.waitForFunction(() => [...document.querySelectorAll("label")].some((label) => {
    return label.textContent.includes("Приоритет") && label.querySelector("select")?.value === "medium";
  }));
  assert.equal(await description.inputValue(), "Обновлено на сервере", "clean description did not refresh");
  await page.getByRole("button", { name: "#2 Задача Б" }).click();
  await page.getByText("Задача #2", { exact: true }).waitFor();
  assert.equal(await description.inputValue(), "Второе описание");
  await page.getByRole("button", { name: "#1 Задача А" }).click();
  await page.getByText("Задача #1", { exact: true }).waitFor();
  assert.equal(await description.inputValue(), "Обновлено на сервере", "server text was not loaded on reopening");
  console.log("PASS: description draft survives refresh and in-flight save, and stays isolated by task");
} finally {
  releaseSave();
  if (browser) {
    await browser.close();
  }
  server.kill();
}
