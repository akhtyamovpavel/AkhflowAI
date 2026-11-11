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
const tasks = [1, 2].map((id) => ({
  id,
  title: `Task ${id}`,
  description: `Description ${id}`,
  status: "backlog",
  priority: "medium",
}));
const pageErrors = [];
const pendingRoutes = new Set();
let mode = "fail";
let detailCalls = 0;
let held;
let browser;

try {
  let ready = false;
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      ready = (await fetch(url)).ok;
    } catch {
      await delay(100);
    }
    if (ready) break;
  }
  assert.ok(ready, "Vite did not start");
  browser = await playwright.chromium.launch({ executablePath: "/usr/bin/google-chrome", headless: true });
  const page = await browser.newPage();
  page.setDefaultTimeout(5000);
  page.on("pageerror", (error) => pageErrors.push(error.message));
  await page.addInitScript(() => {
    if (!sessionStorage.getItem("fixture.started")) {
      localStorage.setItem("akhflow.token", "fixture-token");
      localStorage.setItem("akhflow.project", "alpha");
      sessionStorage.setItem("fixture.started", "yes");
    }
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
    } else if (path === "/api/tasks/1" || path === "/api/tasks/2") {
      detailCalls++;
      const id = Number(path.split("/").at(-1));
      if (id === 2 && mode === "unauthorized") {
        await route.fulfill({ status: 401, json: { error: "invalid token" } });
        return;
      }
      if (id === 1 && mode === "fail") {
        await route.fulfill({ status: 503, json: { error: "fixture failure" } });
        return;
      }
      if (id === 1 && mode === "hold") {
        held = route;
        pendingRoutes.add(route);
        return;
      }
      data = { task: tasks[id - 1], comments: [], dependencies: [], blocks: [], history: [] };
    }
    await route.fulfill({ status: 200, json: data });
  });

  await page.goto(url, { timeout: 30000 });
  await page.getByRole("button", { name: "Task 1", exact: true }).click();
  await page.getByRole("alert").getByText("Не удалось загрузить карточку задачи. Попробуй ещё раз.").waitFor();
  const retry = page.getByRole("button", { name: "Повторить загрузку", exact: true });
  assert.equal(await page.getByRole("button", { name: "Закрыть карточку задачи" }).count(), 1);
  mode = "hold";
  await retry.click();
  await page.getByRole("status").getByText("Загружаем карточку задачи...").waitFor();
  for (let attempt = 0; attempt < 100 && !held; attempt++) {
    await delay(10);
  }
  assert.ok(held, "retry did not start");
  const callsAtRetry = detailCalls;
  await page.waitForTimeout(100);
  assert.equal(detailCalls, callsAtRetry, "retry issued a duplicate request");
  const route = held;
  held = undefined;
  pendingRoutes.delete(route);
  await route.fulfill({ status: 200, json: { task: tasks[0], comments: [], dependencies: [], blocks: [], history: [] } });
  await page.getByRole("textbox", { name: "Заголовок задачи" }).waitFor();
  assert.equal(await page.getByRole("textbox", { name: "Заголовок задачи" }).inputValue(), "Task 1");
  assert.equal(await page.getByText("Не удалось загрузить карточку задачи.", { exact: false }).count(), 0);

  await page.getByRole("button", { name: "Закрыть карточку задачи" }).click();
  await page.getByRole("button", { name: "Task 1", exact: true }).click();
  for (let attempt = 0; attempt < 100 && !held; attempt++) {
    await delay(10);
  }
  assert.ok(held, "second detail request did not start");
  await page.getByRole("button", { name: "Закрыть карточку задачи" }).click();
  await page.getByRole("button", { name: "Task 2", exact: true }).click();
  await page.getByRole("textbox", { name: "Заголовок задачи" }).waitFor();
  const stale = held;
  held = undefined;
  pendingRoutes.delete(stale);
  await stale.fulfill({ status: 401, json: { error: "old session" } }).catch(() => {});
  await page.waitForTimeout(100);
  assert.equal(await page.getByRole("textbox", { name: "Заголовок задачи" }).inputValue(), "Task 2");
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-token");
  await page.getByRole("button", { name: "Закрыть карточку задачи" }).click();
  await page.getByRole("button", { name: "Task 1", exact: true }).click();
  for (let attempt = 0; attempt < 100 && !held; attempt++) {
    await delay(10);
  }
  assert.ok(held, "account switch request did not start");
  await page.evaluate(() => {
    localStorage.setItem("akhflow.token", "second-token");
    window.dispatchEvent(new StorageEvent("storage", {
      key: "akhflow.token",
      newValue: "second-token",
      storageArea: localStorage,
    }));
  });
  await page.getByRole("button", { name: "Task 2", exact: true }).click();
  await page.getByRole("textbox", { name: "Заголовок задачи" }).waitFor();
  const oldAccount = held;
  held = undefined;
  pendingRoutes.delete(oldAccount);
  await oldAccount.fulfill({ status: 401, json: { error: "old account" } }).catch(() => {});
  await page.waitForTimeout(100);
  assert.equal(await page.getByRole("textbox", { name: "Заголовок задачи" }).inputValue(), "Task 2");
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "second-token");
  await page.getByRole("button", { name: "Закрыть карточку задачи" }).click();
  mode = "unauthorized";
  await page.getByRole("button", { name: "Task 2", exact: true }).click();
  await page.waitForFunction(() => localStorage.getItem("akhflow.token") === null);
  await page.getByRole("button", { name: "Войти", exact: true }).waitFor();
  assert.deepEqual(pageErrors, []);
  console.log("PASS: task detail failure can be retried and stale responses cannot replace a later task or log out its session");
} finally {
  for (const route of pendingRoutes) {
    await route.abort().catch(() => {});
  }
  if (browser) await browser.close();
  server.kill("SIGTERM");
}
