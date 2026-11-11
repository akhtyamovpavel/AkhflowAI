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
  description: "",
  status: "backlog",
  priority: "medium",
}));
let attachments = [];
let holdNextList = true;
let failNextList = false;
let held;
const heldRoutes = new Set();
const initialRoutes = new Map();
let delayedInitials = 0;
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
  page.setDefaultTimeout(5000);
  page.on("pageerror", (error) => pageErrors.push(error.message));
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "fixture-token");
    localStorage.setItem("akhflow.project", "alpha");
    const originalFetch = window.fetch;
    let nextRequest = 0;
    window.fetch = (input, options = {}) => {
      const path = typeof input === "string" ? input : input.url;
      if (path.endsWith("/api/tasks/1/attachments") && options.signal && !options.method) {
        const id = ++nextRequest;
        const headers = new Headers(options.headers);
        headers.set("X-Fixture-Request", String(id));
        window.activeAttachmentRequest = { id, signal: options.signal };
        return originalFetch(input, { ...options, headers });
      }
      return originalFetch(input, options);
    };
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
      const id = Number(path.split("/").at(-1));
      data = { task: tasks[id - 1], dependencies: [tasks[2 - id]], comments: [], blocks: [], history: [] };
    } else if (path === "/api/tasks/1/attachments" && request.method() === "POST") {
      holdNextList = false;
      attachments = [{ id: 10, filename: "uploaded.txt", size_bytes: 4 }];
      data = attachments[0];
    } else if (path === "/api/tasks/1/attachments") {
      if (holdNextList) {
        const snapshot = structuredClone(attachments);
        const id = Number(request.headers()["x-fixture-request"]);
        // StrictMode cancels the first request; force the live one to arrive later.
        if (id % 2 === 0) {
          delayedInitials++;
          await delay(150);
        }
        const entry = { route, snapshot };
        initialRoutes.set(id, entry);
        heldRoutes.add(route);
        return;
      }
      if (failNextList) {
        failNextList = false;
        await route.fulfill({ status: 500, json: { error: "fixture failure" } });
        return;
      }
      data = attachments;
    } else if (path === "/api/tasks/2/attachments") {
      data = [{ id: 20, filename: "second-task.txt", size_bytes: 2 }];
    }
    await route.fulfill({ status: 200, json: data });
  });
  const section = page.getByRole("region", { name: "Вложения", exact: true });
  const upload = async () => {
    const file = { name: "uploaded.txt", mimeType: "text/plain", buffer: Buffer.from("test") };
    await section.locator('input[type="file"]').setInputFiles(file);
    await section.getByRole("button", { name: "Загрузить", exact: true }).click();
  };
  const open = async () => {
    await page.getByRole("button", { name: "Task 1", exact: true }).click();
    await section.getByRole("button", { name: "Загрузить", exact: true }).waitFor();
    const active = await page.evaluate(() => {
      const request = window.activeAttachmentRequest;
      return request && !request.signal.aborted ? request.id : null;
    });
    assert.ok(active, "active attachment GET did not start");
    for (let attempt = 0; attempt < 500 && !held; attempt++) {
      held = initialRoutes.get(active);
      if (held) break;
      await delay(10);
    }
    assert.ok(held, "active attachment GET was not intercepted");
  };
  const release = async (status = 200) => {
    const current = held;
    held = undefined;
    const json = status === 200 ? current.snapshot : { error: "old fixture failure" };
    await current.route.fulfill({ status, json }).catch(() => {});
    heldRoutes.delete(current.route);
    await page.waitForTimeout(100);
  };
  const reset = async () => {
    await page.getByRole("button", { name: "Закрыть карточку задачи", exact: true }).click();
    attachments = [];
    holdNextList = true;
    await open();
  };
  await page.goto(url, { timeout: 30000 });
  await open();
  await upload();
  await section.getByRole("button", { name: "uploaded.txt (4 байт)", exact: true }).waitFor();
  await release();
  assert.equal(await section.getByRole("button", { name: "uploaded.txt (4 байт)", exact: true }).count(), 1);
  assert.equal(await section.getByText("Вложений пока нет.", { exact: true }).count(), 0);
  assert.equal(await section.getByText("Загружаем вложения…", { exact: true }).count(), 0);

  await reset();
  await upload();
  await section.getByRole("button", { name: "uploaded.txt (4 байт)", exact: true }).waitFor();
  await release(500);
  assert.equal(await section.getByRole("alert").count(), 0, "stale failure replaced a successful refresh");

  await reset();
  failNextList = true;
  await upload();
  await section.getByRole("alert").getByText("Файл загружен, но список вложений не обновился.", { exact: true }).waitFor();
  await release();
  assert.equal(await section.getByRole("alert").count(), 1, "stale success hid the latest failure");
  assert.equal(await section.getByText("Загружаем вложения…", { exact: true }).count(), 0);

  await reset();
  await release(500);
  await section.getByRole("alert").getByText("Не удалось загрузить список вложений.", { exact: true }).waitFor();
  await upload();
  await section.getByRole("button", { name: "uploaded.txt (4 байт)", exact: true }).waitFor();
  assert.equal(await section.getByRole("alert").count(), 0, "successful refresh did not clear the previous failure");

  await reset();
  await page.getByRole("button", { name: "#2 Task 2", exact: true }).click();
  await section.getByRole("button", { name: "second-task.txt (2 байт)", exact: true }).waitFor();
  await release(500);
  assert.equal(await section.getByRole("button", { name: "second-task.txt (2 байт)", exact: true }).count(), 1);
  assert.equal(await section.getByRole("alert").count(), 0, "previous task failure affected the selected task");
  assert.ok(delayedInitials > 0, "the live initial request was never delayed");
  assert.deepEqual(pageErrors, []);
  console.log("PASS: attachment refresh ignores stale success and failure responses and isolates tasks");
} finally {
  for (const route of heldRoutes) {
    await route.abort().catch(() => {});
  }
  if (browser) {
    await browser.close();
  }
  server.kill();
}
