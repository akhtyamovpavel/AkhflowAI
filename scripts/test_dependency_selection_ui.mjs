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
const tasks = [1, 2, 3, 4].map((id) => ({
  id,
  title: `Task ${id}`,
  description: "",
  status: "backlog",
  priority: "medium",
}));
const dependencies = new Set();
const pending = new Map();
const writes = [];
const pageErrors = [];
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
  page.setDefaultTimeout(5000);
  page.on("pageerror", (error) => pageErrors.push(error.message));
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "fixture-token");
    localStorage.setItem("akhflow.project", "alpha");
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path.endsWith("/events")) {
      await route.fulfill({ status: 403, body: "" });
      return;
    }
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Alpha" }];
    } else if (path === "/api/projects/alpha/tasks") {
      data = tasks;
    } else if (path === "/api/projects/alpha/role") {
      data = { role: "owner" };
    } else if (path === "/api/tasks/1/dependencies" && request.method() === "POST") {
      const id = request.postDataJSON().depends_on;
      writes.push(id);
      await new Promise((resolve) => pending.set(id, resolve));
      pending.delete(id);
      dependencies.add(id);
      data = { task_id: 1, depends_on: id };
    } else if (path === "/api/tasks/1") {
      const deps = tasks.filter((task) => dependencies.has(task.id));
      data = { task: tasks[0], comments: [], dependencies: deps, blocks: [], history: [] };
    }
    await route.fulfill({ status: 200, json: data });
  });
  await page.goto(url);
  await page.getByRole("button", { name: "Task 1", exact: true }).click();
  const placeholder = page.locator("option").filter({ hasText: "Выберите задачу..." });
  const selection = page.locator("select").filter({ has: placeholder });
  const add = page.locator(".fixed.inset-0.z-50").getByRole("button", { name: "Добавить", exact: true });
  const waitForPending = async (id) => {
    for (let attempt = 0; attempt < 100 && !pending.has(id); attempt++) {
      await delay(10);
    }
    assert.ok(pending.has(id), `POST for dependency ${id} did not start`);
  };
  const release = async (id) => {
    pending.get(id)();
    await page.getByRole("button", { name: `#${id} Task ${id}`, exact: true }).waitFor();
  };

  await selection.selectOption("2");
  await add.click();
  await waitForPending(2);
  await selection.selectOption("3");
  await release(2);
  assert.equal(await selection.inputValue(), "3", "completed POST erased the next dependency selection");
  await add.click();
  await waitForPending(3);
  await release(3);
  assert.equal(await selection.inputValue(), "", "submitted selection was not cleared");
  assert.deepEqual(writes, [2, 3], "the next selection was not submitted");

  await page.getByRole("button", { name: "Закрыть карточку задачи", exact: true }).click();
  dependencies.clear();
  await page.getByRole("button", { name: "Task 1", exact: true }).click();
  await selection.selectOption("2");
  await add.click();
  await waitForPending(2);
  await selection.selectOption("3");
  await add.click();
  await waitForPending(3);
  await selection.selectOption("4");
  await release(3);
  assert.equal(await selection.inputValue(), "4", "newer POST erased a subsequent selection");
  await release(2);
  assert.equal(await selection.inputValue(), "4", "older POST erased a subsequent selection");
  await add.click();
  await waitForPending(4);
  await release(4);
  assert.equal(await selection.count(), 0, "all dependencies should have been added");
  assert.deepEqual(writes.slice(2), [2, 3, 4]);
  assert.deepEqual(pageErrors, []);
  console.log("PASS: delayed dependency additions preserve later selections and concurrent responses");
} finally {
  for (const resolve of pending.values()) {
    resolve();
  }
  if (browser) {
    await browser.close();
  }
  server.kill();
}
