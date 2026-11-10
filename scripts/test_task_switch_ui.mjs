import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { setTimeout as delay } from "node:timers/promises";
import { pathToFileURL } from "node:url";

const moduleName = process.env.PLAYWRIGHT_MODULE;
const playwright = await import(moduleName ? pathToFileURL(moduleName).href : "playwright");
const probe = createServer();
await new Promise((resolve) => probe.listen(0, "127.0.0.1", resolve));
const port = probe.address().port;
await new Promise((resolve) => probe.close(resolve));
const url = `http://127.0.0.1:${port}`;
const server = spawn("./node_modules/.bin/vite", ["--host", "127.0.0.1", "--port", String(port), "--strictPort"], {
  cwd: new URL("../web", import.meta.url),
  stdio: "ignore",
});

let releasePatch;
const patchWait = new Promise((resolve) => { releasePatch = resolve; });
let patchStarted;
const patchReceived = new Promise((resolve) => { patchStarted = resolve; });
let staleLoaded;
const staleResponse = new Promise((resolve) => { staleLoaded = resolve; });
let releaseSecondTask;
const secondTaskWait = new Promise((resolve) => { releaseSecondTask = resolve; });
let secondTaskStarted;
const secondTaskReceived = new Promise((resolve) => { secondTaskStarted = resolve; });

const first = { id: 1, title: "Задача А", status: "backlog", priority: "medium" };
const second = { id: 2, title: "Задача Б", status: "todo", priority: "medium" };
let holdSecondTask = false;
let patchReleased = false;
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
      patchStarted();
      await patchWait;
      data = first;
    } else if (path === "/api/tasks/1") {
      let task = first;
      if (patchReleased) {
        task = { ...first, title: "Устаревшая задача А" };
      }
      data = { task, dependencies: [second], comments: [], blocks: [], history: [] };
    } else if (path === "/api/tasks/2") {
      if (holdSecondTask) {
        secondTaskStarted();
        await secondTaskWait;
      }
      data = { task: second, dependencies: [first], comments: [], blocks: [], history: [] };
    }
    await route.fulfill({ status: 200, json: data }).catch(() => {});
    if (path === "/api/tasks/1" && request.method() === "GET" && patchReleased) {
      staleLoaded();
    }
  });

  await page.goto(url);
  await page.getByRole("button", { name: "Задача А", exact: true }).click();
  await page.getByText("Задача #1", { exact: true }).waitFor();
  await page.getByPlaceholder("Комментарий...").fill("Черновик для А");
  await page.getByPlaceholder("Описание задачи...").fill("Изменённое описание");
  await page.getByRole("button", { name: "Сохранить", exact: true }).click();
  await patchReceived;
  await page.getByRole("button", { name: "#2 Задача Б" }).click();
  await page.getByText("Задача #2", { exact: true }).waitFor();
  assert.equal(await page.getByPlaceholder("Комментарий...").inputValue(), "");
  await page.getByRole("button", { name: "#1 Задача А" }).click();
  await page.getByText("Задача #1", { exact: true }).waitFor();
  assert.equal(await page.getByPlaceholder("Комментарий...").inputValue(), "");
  patchReleased = true;
  releasePatch();
  await staleResponse;
  await delay(100);
  const drawer = page.locator(".fixed.inset-0.z-50");
  assert.equal(await drawer.locator("input").first().inputValue(), "Задача А");

  holdSecondTask = true;
  await page.getByRole("button", { name: "#2 Задача Б" }).click();
  await secondTaskReceived;
  assert.equal(await page.getByText("Задача #1", { exact: true }).count(), 0);
  assert.equal(await page.getByPlaceholder("Описание задачи...").count(), 0);
  releaseSecondTask();
  await page.getByText("Задача #2", { exact: true }).waitFor();
  assert.equal(await drawer.locator("input").first().inputValue(), "Задача Б");
  console.log("PASS: task switching isolates drafts, pending saves and delayed responses");
} finally {
  releasePatch();
  releaseSecondTask();
  if (browser) {
    await browser.close();
  }
  server.kill();
}
