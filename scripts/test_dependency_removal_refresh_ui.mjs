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
const task = { id: 1, title: "Current task", description: "Saved description", status: "backlog", priority: "medium" };
const dependency = { ...task, id: 2, title: "Dependency task" };
let linked = true;
let boardMode = "success";
let boardRequests = 0;
let deletes = 0;
let releaseBoard;
let browser;
const errors = [];

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
  page.on("pageerror", (error) => errors.push(error.message));
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
      boardRequests++;
      if (boardMode === "http-failure") {
        await route.fulfill({ status: 500, json: { error: "fixture board failure" } });
        return;
      }
      if (boardMode === "network-failure") {
        await route.abort("failed");
        return;
      }
      if (boardMode === "hold") {
        await new Promise((resolve) => { releaseBoard = resolve; });
      }
      data = [{ ...task, depends_on: linked ? [2] : [] }, dependency];
    } else if (path === "/api/tasks/1/dependencies/2" && request.method() === "DELETE") {
      deletes++;
      linked = false;
      data = { status: "removed" };
    } else if (path === "/api/tasks/1" && request.method() === "PATCH") {
      await route.fulfill({ status: 500, json: { error: "keep the title draft" } });
      return;
    } else if (path === "/api/tasks/1") {
      data = { task, dependencies: linked ? [dependency] : [], comments: [], blocks: [], history: [] };
    } else if (path.endsWith("/role")) {
      data = { role: "owner" };
    }
    await route.fulfill({ json: data });
  });
  await page.goto(url, { timeout: 30000 });
  await page.getByRole("button", { name: task.title, exact: true }).click();
  const title = page.getByRole("textbox", { name: "Заголовок задачи", exact: true });
  const description = page.getByPlaceholder("Описание задачи...");
  const comment = page.getByPlaceholder("Комментарий...");
  await description.fill("New description draft");
  await comment.fill("New comment draft");
  await title.fill("New title draft");
  await description.click();
  await page.getByRole("alert").filter({ hasText: "Не удалось сохранить заголовок" }).waitFor();

  boardMode = "http-failure";
  await page.getByRole("button", { name: "Удалить связь с задачей #2: Dependency task", exact: true }).click();
  const refreshError = page.getByRole("alert").filter({ hasText: "Связь удалена, но обновить данные не удалось" });
  await refreshError.waitFor();
  assert.equal(deletes, 1);
  assert.equal(linked, false);
  assert.equal(await page.getByText("ждёт: 1", { exact: true }).count(), 1, "fixture board should remain stale after GET failure");
  assert.deepEqual(errors, [], "onChanged rejection escaped the removal handler");
  const retry = page.getByRole("button", { name: "Проверить удаление связи", exact: true });

  boardMode = "network-failure";
  const beforeFailure = boardRequests;
  await retry.click();
  await retry.waitFor();
  await page.waitForFunction(() => {
    const button = [...document.querySelectorAll("button")].find((item) => item.textContent === "Проверить удаление связи");
    return button && !button.disabled;
  });
  assert.equal(boardRequests, beforeFailure + 1);
  assert.equal(await refreshError.count(), 1);
  assert.equal(deletes, 1, "failed refresh retried DELETE");

  boardMode = "hold";
  const beforeSuccess = boardRequests;
  await retry.click();
  for (let attempt = 0; attempt < 100 && !releaseBoard; attempt++) {
    await delay(10);
  }
  assert.ok(releaseBoard, "retry did not request the board");
  assert.equal(await retry.isDisabled(), true, "retry remained enabled during refresh");
  releaseBoard();
  await refreshError.waitFor({ state: "hidden" });
  await page.getByRole("status").filter({ hasText: "Проверка завершена: связь отсутствует." }).waitFor();
  assert.equal(boardRequests, beforeSuccess + 1);
  assert.equal(await page.getByText("ждёт: 1", { exact: true }).count(), 0, "successful retry did not refresh the board");
  assert.equal(deletes, 1, "successful refresh retried DELETE");
  assert.equal(await title.inputValue(), "New title draft");
  assert.equal(await description.inputValue(), "New description draft");
  assert.equal(await comment.inputValue(), "New comment draft");
  assert.deepEqual(errors, []);
  console.log("PASS: dependency removal handles board refresh errors and retries GET without losing drafts");
} finally {
  if (releaseBoard) {
    releaseBoard();
  }
  if (browser) {
    await browser.close();
  }
  server.kill();
}
