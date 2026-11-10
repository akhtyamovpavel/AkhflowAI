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
  const tasks = [{ id: 1, title: "Первая задача", status: "todo", priority: "medium" }];
  let moves = 0;
  let failMove = false;
  let slowMove = false;
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Альфа" }];
    } else if (path === "/api/projects/alpha/tasks") {
      if (request.method() === "POST") {
        tasks.push({ id: 2, title: request.postDataJSON().title, status: "backlog", priority: "medium" });
        data = tasks[1];
      } else {
        data = tasks;
      }
    } else if (path === "/api/tasks/1" && request.method() === "PATCH") {
      moves++;
      if (slowMove) {
        await delay(300);
      }
      if (failMove) {
        await route.fulfill({ status: 409, body: "Задача заблокирована" });
        return;
      }
      tasks[0].status = request.postDataJSON().status;
      data = tasks[0];
    } else if (path === "/api/tasks/1") {
      data = { task: tasks[0], comments: [], dependencies: [], blocks: [], history: [] };
    }
    await route.fulfill({ status: 200, json: data });
  });
  await page.goto(url);
  const card = page.getByRole("button", { name: "Первая задача", exact: true });
  const columns = page.locator("div.grid.grid-cols-4 > div");
  await card.waitFor();
  await card.focus();
  await page.keyboard.press("ArrowRight");
  await columns.nth(2).getByRole("button", { name: "Первая задача", exact: true }).waitFor();
  await page.waitForFunction(() => document.activeElement?.textContent === "Первая задача");
  assert.equal(tasks[0].status, "in_progress");
  assert.equal(moves, 1);
  await page.keyboard.press("ArrowLeft");
  await columns.nth(1).getByRole("button", { name: "Первая задача", exact: true }).waitFor();
  await page.waitForFunction(() => document.activeElement?.textContent === "Первая задача");
  assert.equal(tasks[0].status, "todo");
  assert.equal(moves, 2);
  slowMove = true;
  await page.keyboard.press("ArrowLeft");
  const listButton = page.getByRole("button", { name: "Список", exact: true });
  await listButton.focus();
  await columns.nth(0).getByRole("button", { name: "Первая задача", exact: true }).waitFor();
  assert.equal(await listButton.evaluate((element) => document.activeElement === element), true);
  slowMove = false;
  await card.focus();
  await page.keyboard.press("ArrowLeft");
  assert.equal(moves, 3);
  await page.keyboard.press("ArrowRight");
  await columns.nth(1).getByRole("button", { name: "Первая задача", exact: true }).waitFor();
  failMove = true;
  await page.keyboard.press("ArrowRight");
  await page.getByRole("alert").filter({ hasText: "Задача заблокирована" }).waitFor();
  assert.equal(tasks[0].status, "todo");
  failMove = false;
  const input = page.getByPlaceholder("Новая задача...");
  await page.keyboard.press("Alt+KeyN");
  assert.equal(await input.evaluate((element) => document.activeElement === element), true);
  await input.fill("Новая задача");
  await page.keyboard.press("ArrowRight");
  await page.keyboard.press("Alt+KeyN");
  assert.equal(moves, 5);
  await page.keyboard.press("Enter");
  await page.getByRole("button", { name: "Новая задача", exact: true }).waitFor();
  await card.click();
  await page.getByText("Задача #1").waitFor();
  await page.keyboard.press("Alt+KeyN");
  assert.equal(await input.evaluate((element) => document.activeElement === element), false);
  console.log("PASS: keyboard movement, focus, errors, task creation and modal guard");
} finally {
  if (browser) {
    await browser.close();
  }
  server.kill();
}
