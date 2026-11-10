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
let browser;
let releasePatch;

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
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "fixture-token");
    localStorage.setItem("akhflow.project", "alpha");
  });
  const task = { id: 1, title: "Initial title", description: "Saved description", status: "backlog", priority: "medium" };
  let mode = "reject";
  let failRefresh = false;
  const writes = [];
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path.endsWith("/events")) {
      await route.fulfill({ status: 403, body: "" });
      return;
    }
    if (path === "/api/tasks/1" && request.method() === "PATCH") {
      const body = request.postDataJSON();
      if ("title" in body) {
        writes.push(body.title);
        if (mode === "reject") {
          await route.fulfill({ status: 500, json: { error: "fixture failure" } });
          return;
        }
        if (mode === "network") {
          await route.abort("failed");
          return;
        }
        if (mode === "hold") {
          await new Promise((resolve) => { releasePatch = resolve; });
        }
      }
      Object.assign(task, body);
      await route.fulfill({ json: { status: "updated" } });
      return;
    }
    if (path === "/api/tasks/1" && failRefresh) {
      await route.fulfill({ status: 500, json: { error: "refresh failure" } });
      return;
    }
    let data = [];
    if (path === "/api/projects") data = [{ id: 1, slug: "alpha", name: "Alpha" }];
    if (path === "/api/projects/alpha/tasks") data = [task];
    if (path === "/api/tasks/1") data = { task, comments: [], dependencies: [], blocks: [], history: [] };
    if (path.endsWith("/role")) data = { role: "owner" };
    await route.fulfill({ json: data });
  });
  await page.goto(url);
  await page.getByRole("button", { name: "Initial title", exact: true }).click();
  const title = page.getByRole("textbox", { name: "Заголовок задачи", exact: true });
  const description = page.getByPlaceholder("Описание задачи...");
  const saveError = page.getByRole("alert").filter({ hasText: "Не удалось сохранить заголовок" });
  await title.focus();
  await description.click();
  assert.deepEqual(writes, [], "unchanged title was sent to the server");

  await title.fill("Retry this title");
  await description.click();
  await saveError.waitFor();
  assert.equal(await title.inputValue(), "Retry this title");
  assert.equal(task.title, "Initial title");
  const refreshed = page.waitForResponse((response) => response.url().endsWith("/api/tasks/1") && response.request().method() === "GET");
  await page.locator(".fixed.inset-0.z-50 select").nth(1).selectOption("high");
  await refreshed;
  assert.equal(await title.inputValue(), "Retry this title", "unrelated refresh erased the failed title draft");
  mode = "success";
  await page.getByRole("button", { name: "Повторить сохранение", exact: true }).click();
  await saveError.waitFor({ state: "hidden" });
  await page.getByRole("button", { name: "Retry this title", exact: true }).waitFor();
  assert.equal(task.title, "Retry this title");
  assert.deepEqual(writes, ["Retry this title", "Retry this title"]);
  await page.getByRole("button", { name: "Закрыть карточку задачи", exact: true }).click();
  await page.getByRole("button", { name: "Retry this title", exact: true }).click();
  assert.equal(await title.inputValue(), "Retry this title");

  mode = "network";
  await title.fill("Network retry");
  await title.press("Enter");
  await saveError.waitFor();
  assert.equal(await title.inputValue(), "Network retry");
  mode = "success";
  await page.getByRole("button", { name: "Повторить сохранение", exact: true }).click();
  await page.getByRole("button", { name: "Network retry", exact: true }).waitFor();

  mode = "hold";
  await title.fill("Pending title");
  await description.click();
  await page.getByRole("status").filter({ hasText: "Сохраняем заголовок" }).waitFor();
  while (!releasePatch) await delay(10);
  await title.fill("Newer draft");
  mode = "success";
  releasePatch();
  await page.getByRole("status").filter({ hasText: "Сохраняем заголовок" }).waitFor({ state: "hidden" });
  assert.equal(task.title, "Pending title");
  assert.equal(await title.inputValue(), "Newer draft", "pending save erased a newer edit");
  const beforeRetry = writes.length;
  await page.getByRole("button", { name: "Сохранить заголовок", exact: true }).click();
  await page.getByRole("button", { name: "Newer draft", exact: true }).waitFor();
  assert.equal(task.title, "Newer draft");
  assert.equal(writes.length, beforeRetry + 1, "blur and retry click duplicated the save");

  failRefresh = true;
  await title.fill("Saved despite refresh failure");
  await description.click();
  await page.getByRole("alert").filter({ hasText: "Не удалось обновить карточку после сохранения заголовка" }).waitFor();
  assert.equal(task.title, "Saved despite refresh failure");
  assert.equal(await title.inputValue(), "Saved despite refresh failure");
  assert.equal(await saveError.count(), 0);
  assert.deepEqual(errors, [], "title saving produced an unhandled error");
  console.log("PASS: title failures retain drafts, retries persist changes, pending saves preserve newer edits");
} finally {
  releasePatch?.();
  if (browser) await browser.close();
  server.kill();
}
