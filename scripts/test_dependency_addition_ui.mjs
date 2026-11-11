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
let releaseAdd;

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
  const candidates = [2, 3, 4].map((id) => ({ ...current, id, title: `Candidate ${id}` }));
  const dependencies = [];
  const writes = [];
  let mode = "conflict";
  let rejectRefresh = false;
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path.endsWith("/events")) {
      await route.fulfill({ status: 403, body: "" });
      return;
    }
    if (path === "/api/tasks/1/dependencies" && request.method() === "POST") {
      const id = request.postDataJSON().depends_on;
      writes.push(id);
      let result = mode;
      if (result === "hold") {
        result = await new Promise((resolve) => { releaseAdd = resolve; });
      }
      if (result === "conflict" || result === "server") {
        const status = result === "conflict" ? 409 : 500;
        await route.fulfill({ status, json: { error: "fixture failure" } });
        return;
      }
      if (result === "network") {
        await route.abort("failed");
        return;
      }
      if (!dependencies.some((task) => task.id === id)) {
        dependencies.push(candidates.find((task) => task.id === id));
      }
      if (result === "accepted-network") {
        await route.abort("failed");
        return;
      }
      await route.fulfill({ json: { status: "added" } });
      return;
    }
    if (path === "/api/tasks/1" && rejectRefresh) {
      await route.fulfill({ status: 500, json: { error: "refresh failure" } });
      return;
    }
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Alpha" }];
    }
    if (path === "/api/projects/alpha/tasks") {
      data = [current, ...candidates];
    }
    if (path === "/api/tasks/1") {
      data = { task: current, dependencies, blocks: [], comments: [], history: [] };
    }
    if (path.endsWith("/role")) {
      data = { role: "owner" };
    }
    await route.fulfill({ json: data });
  });
  await page.goto(url);
  await page.getByRole("button", { name: current.title, exact: true }).click();
  const select = page.getByRole("combobox", { name: "Новая зависимость", exact: true });
  const add = page.locator(".fixed.inset-0.z-50").getByRole("button", { name: "Добавить", exact: true });
  const uncertain = page.getByRole("alert").filter({ hasText: "Не удалось подтвердить добавление зависимости" });
  const busy = page.getByRole("status").filter({ hasText: "Обновляем зависимости" });
  const refresh = page.getByRole("button", { name: "Обновить связи", exact: true });

  mode = "hold";
  await select.selectOption("2");
  await add.click();
  while (!releaseAdd) {
    await delay(10);
  }
  await select.selectOption("3");
  releaseAdd("conflict");
  await page.getByRole("alert").filter({ hasText: "связь создаёт цикл" }).waitFor();
  assert.equal(await select.inputValue(), "3", "409 erased the newer dependency selection");
  assert.equal(dependencies.length, 0);

  mode = "server";
  await select.selectOption("2");
  await add.click();
  await uncertain.waitFor();
  assert.equal(await select.inputValue(), "2", "500 erased dependency selection");
  const beforeCheck = writes.length;
  await refresh.click();
  await uncertain.waitFor({ state: "hidden" });
  assert.equal(writes.length, beforeCheck, "checking uncertain result repeated POST");
  assert.equal(await select.inputValue(), "2");
  mode = "success";
  await add.click();
  await page.getByRole("button", { name: "#2 Candidate 2", exact: true }).waitFor();
  await busy.waitFor({ state: "hidden" });
  assert.equal(await select.inputValue(), "");

  mode = "network";
  await select.selectOption("3");
  await add.click();
  await uncertain.waitFor();
  assert.equal(await select.inputValue(), "3", "network failure erased dependency selection");
  assert.ok((await uncertain.innerText()).includes("Обнови связи перед повторной отправкой"));
  await refresh.click();
  await uncertain.waitFor({ state: "hidden" });
  assert.equal(await select.inputValue(), "3");

  mode = "hold";
  releaseAdd = undefined;
  const beforePending = writes.length;
  await add.dblclick({ force: true });
  await busy.waitFor();
  while (!releaseAdd) {
    await delay(10);
  }
  assert.equal(await select.isDisabled(), false);
  assert.equal(await add.isDisabled(), true);
  assert.equal(writes.length, beforePending + 1, "double click duplicated dependency POST");
  await select.selectOption("4");
  assert.equal(await add.isDisabled(), false, "new dependency selection should remain available");
  releaseAdd("accepted-network");
  await uncertain.waitFor();
  assert.equal(await select.inputValue(), "4", "uncertain response erased the newer selection");
  const beforeAcceptedCheck = writes.length;
  await refresh.click();
  await page.getByRole("button", { name: "#3 Candidate 3", exact: true }).waitFor();
  await busy.waitFor({ state: "hidden" });
  assert.equal(await select.inputValue(), "4", "checking previous dependency erased the newer selection");
  assert.equal(writes.length, beforeAcceptedCheck, "confirmation check wrote another dependency");
  assert.equal(await page.getByRole("alert").count(), 0);

  mode = "success";
  rejectRefresh = true;
  await select.selectOption("4");
  await add.click();
  await page.getByRole("alert").filter({ hasText: "Зависимость добавлена, но обновить данные не удалось" }).waitFor();
  const beforeRefreshRetry = writes.length;
  rejectRefresh = false;
  await refresh.click();
  await page.getByRole("button", { name: "#4 Candidate 4", exact: true }).waitFor();
  await busy.waitFor({ state: "hidden" });
  assert.equal(writes.length, beforeRefreshRetry, "refresh retry repeated successful dependency creation");
  assert.equal(await page.getByRole("alert").count(), 0);
  assert.deepEqual(errors, [], "adding dependencies produced an unhandled error");
  console.log("PASS: dependency errors retain selection, retries succeed and uncertain results can be checked without POST");
} finally {
  releaseAdd?.("success");
  if (browser) {
    await browser.close();
  }
  server.kill();
}
