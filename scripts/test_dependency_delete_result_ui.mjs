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
  const current = { id: 1, title: "Current task", status: "todo", priority: "medium", description: "Saved description" };
  const prerequisite = { ...current, id: 2, title: "Prerequisite" };
  const dependent = { ...current, id: 3, title: "Dependent" };
  let dependencies = [prerequisite];
  let blocks = [dependent];
  let mode = "success";
  let failedReads = 0;
  const writes = [];
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path.endsWith("/events")) {
      await route.fulfill({ status: 403, body: "" });
      return;
    }
    if (request.method() === "DELETE") {
      writes.push(path);
      if (mode === "server") {
        await route.fulfill({ status: 500, json: { error: "fixture failure" } });
        return;
      }
      if (mode === "network") {
        await route.abort("failed");
        return;
      }
      if (path === "/api/tasks/1/dependencies/2") {
        dependencies = [];
      } else if (path === "/api/tasks/3/dependencies/1") {
        blocks = [];
      } else {
        throw new Error("unexpected dependency DELETE");
      }
      if (mode === "accepted-network") {
        await route.abort("failed");
        return;
      }
      await route.fulfill({ json: { status: "deleted" } });
      return;
    }
    if (path === "/api/tasks/1" && failedReads > 0) {
      failedReads--;
      await route.fulfill({ status: 500, json: { error: "detail refresh failure" } });
      return;
    }
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Alpha" }];
    }
    if (path === "/api/projects/alpha/tasks") {
      data = [current, prerequisite, dependent];
    }
    if (path === "/api/tasks/1") {
      data = { task: current, dependencies, blocks, comments: [], history: [] };
    }
    if (path.endsWith("/role")) {
      data = { role: "owner" };
    }
    await route.fulfill({ json: data });
  });
  await page.goto(url);
  const open = page.getByRole("button", { name: current.title, exact: true });
  await open.click();
  const removePrerequisite = page.getByRole("button", { name: "Удалить связь с задачей #2: Prerequisite", exact: true });
  const removeDependent = page.getByRole("button", { name: "Удалить связь с задачей #3: Dependent", exact: true });
  const check = page.getByRole("button", { name: "Проверить удаление связи", exact: true });
  const confirmedFailure = page.getByRole("alert").filter({ hasText: "Связь удалена, но обновить данные не удалось" });
  const uncertain = page.getByRole("alert").filter({ hasText: "Не удалось подтвердить удаление связи" });
  const verifyAbsent = async () => {
    await page.getByRole("status").filter({ hasText: "Проверка завершена: связь отсутствует." }).waitFor();
    assert.equal(await page.getByRole("alert").count(), 0);
  };
  const reloadCard = async () => {
    await page.getByRole("button", { name: "Закрыть карточку задачи", exact: true }).click();
    await open.click();
    await removePrerequisite.waitFor();
  };

  failedReads = 2;
  await removePrerequisite.click();
  await confirmedFailure.waitFor();
  assert.equal(writes.length, 1);
  assert.equal(dependencies.length, 0, "DELETE was not committed in fixture");
  assert.equal(await removePrerequisite.isDisabled(), true, "stale row can repeat a confirmed DELETE");
  await check.click();
  await confirmedFailure.waitFor();
  assert.equal(writes.length, 1, "failed verification repeated DELETE");
  await check.click();
  await verifyAbsent();
  assert.equal(await removePrerequisite.count(), 0);
  assert.deepEqual(writes, ["/api/tasks/1/dependencies/2"]);

  mode = "accepted-network";
  await removeDependent.click();
  await uncertain.waitFor();
  assert.equal(blocks.length, 0);
  assert.equal(await removeDependent.isDisabled(), true);
  failedReads = 1;
  await check.click();
  await page.getByRole("alert").filter({ hasText: "Не удалось обновить данные о связях" }).waitFor();
  assert.equal(writes.length, 2);
  await check.click();
  await verifyAbsent();
  assert.equal(await removeDependent.count(), 0);
  assert.equal(writes[1], "/api/tasks/3/dependencies/1", "incoming dependency used wrong DELETE direction");
  assert.equal(writes.length, 2);

  dependencies = [prerequisite];
  await reloadCard();
  mode = "network";
  await removePrerequisite.click();
  await uncertain.waitFor();
  assert.equal(dependencies.length, 1);
  await check.click();
  await page.getByRole("status").filter({ hasText: "Проверка завершена: связь присутствует" }).waitFor();
  assert.equal(writes.length, 3);
  assert.equal(await removePrerequisite.isEnabled(), true);
  mode = "success";
  await removePrerequisite.click();
  await verifyAbsent();
  assert.equal(writes.length, 4, "explicit retry should issue exactly one new DELETE");

  dependencies = [prerequisite];
  await reloadCard();
  mode = "server";
  await removePrerequisite.click();
  await uncertain.waitFor();
  await check.click();
  await page.getByRole("status").filter({ hasText: "Проверка завершена: связь присутствует" }).waitFor();
  assert.equal(writes.length, 5);
  mode = "success";
  await removePrerequisite.click();
  await verifyAbsent();
  assert.equal(writes.length, 6);
  assert.deepEqual(errors, [], "dependency removal produced an unhandled error");
  console.log("PASS: confirmed deletion and uncertain results recover through GET without repeating DELETE");
} finally {
  if (browser) {
    await browser.close();
  }
  server.kill();
}
