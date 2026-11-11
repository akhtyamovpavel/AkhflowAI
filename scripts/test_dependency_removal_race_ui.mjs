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
let releaseBoard;

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
  page.setDefaultNavigationTimeout(15000);
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "fixture-token");
    localStorage.setItem("akhflow.project", "alpha");
  });
  const current = { id: 1, title: "Current task", status: "todo", priority: "medium" };
  const prerequisite = { ...current, id: 2, title: "Prerequisite" };
  let linked = true;
  let holdNextBoard = false;
  let abortAddition = false;
  let deletes = 0;
  let additions = 0;
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
    } else if (path === "/api/tasks/1/dependencies/2" && request.method() === "DELETE") {
      deletes++;
      linked = false;
      data = { status: "deleted" };
    } else if (path === "/api/tasks/1/dependencies" && request.method() === "POST") {
      additions++;
      assert.equal(request.postDataJSON().depends_on, 2);
      linked = true;
      if (abortAddition) {
        await route.abort("failed");
        return;
      }
      data = { status: "added" };
    } else if (path === "/api/projects/alpha/tasks") {
      if (holdNextBoard) {
        holdNextBoard = false;
        await new Promise((resolve) => { releaseBoard = resolve; });
      }
      data = [{ ...current, depends_on: linked ? [2] : [] }, prerequisite];
    } else if (path === "/api/tasks/1") {
      data = { task: current, dependencies: linked ? [prerequisite] : [], blocks: [], comments: [], history: [] };
    } else if (path.endsWith("/role")) {
      data = { role: "owner" };
    }
    await route.fulfill({ json: data });
  });
  await page.goto(url);
  await page.getByRole("button", { name: current.title, exact: true }).click();
  const remove = page.getByRole("button", { name: "Удалить связь с задачей #2: Prerequisite", exact: true });
  holdNextBoard = true;
  await remove.click();
  for (let attempt = 0; attempt < 100 && !releaseBoard; attempt++) {
    await delay(10);
  }
  assert.ok(releaseBoard, "removal did not begin the delayed board GET");
  await remove.waitFor({ state: "hidden" });

  const select = page.getByRole("combobox", { name: "Новая зависимость", exact: true });
  await select.selectOption("2");
  await page.locator(".fixed.inset-0.z-50").getByRole("button", { name: "Добавить", exact: true }).click();
  await remove.waitFor();
  await page.getByRole("status").filter({ hasText: "Обновляем зависимости" }).waitFor({ state: "hidden" });
  assert.equal(linked, true);
  releaseBoard();
  const stale = page.getByRole("alert").filter({ hasText: "Данные карточки изменились во время проверки" });
  await stale.waitFor();
  assert.equal(await page.getByRole("status").filter({ hasText: "Проверка завершена: связь отсутствует." }).count(), 0, "old removal snapshot claimed the re-added dependency was absent");
  assert.equal(await remove.isDisabled(), true, "unverified deletion result enabled another DELETE");
  await page.getByRole("button", { name: "Проверить удаление связи", exact: true }).click();
  await page.getByRole("status").filter({ hasText: "Проверка завершена: связь присутствует" }).waitFor();
  assert.equal(await remove.isEnabled(), true);
  assert.equal(deletes, 1);
  assert.equal(additions, 1);

  holdNextBoard = true;
  releaseBoard = undefined;
  await remove.click();
  for (let attempt = 0; attempt < 100 && !releaseBoard; attempt++) {
    await delay(10);
  }
  assert.ok(releaseBoard);
  await remove.waitFor({ state: "hidden" });
  abortAddition = true;
  await select.selectOption("2");
  await page.locator(".fixed.inset-0.z-50").getByRole("button", { name: "Добавить", exact: true }).click();
  await page.getByRole("alert").filter({ hasText: "Не удалось подтвердить добавление зависимости" }).waitFor();
  await page.getByRole("status").filter({ hasText: "Обновляем зависимости" }).waitFor({ state: "hidden" });
  releaseBoard();
  await stale.waitFor();
  assert.equal(await page.getByRole("status").filter({ hasText: "Проверка завершена: связь отсутствует." }).count(), 0, "uncertain re-addition did not invalidate the old removal snapshot");
  await page.getByRole("button", { name: "Проверить удаление связи", exact: true }).click();
  await page.getByRole("status").filter({ hasText: "Проверка завершена: связь присутствует" }).waitFor();
  await remove.waitFor();
  assert.equal(deletes, 2);
  assert.equal(additions, 2);
  assert.deepEqual(errors, []);
  console.log("PASS: removal verification ignores a snapshot superseded by a re-added dependency");
} finally {
  releaseBoard?.();
  if (browser) {
    await browser.close();
  }
  server.kill();
}
