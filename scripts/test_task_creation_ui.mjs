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
let releaseCreate;

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
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "fixture-token");
    localStorage.setItem("akhflow.project", "alpha");
  });
  const writes = [];
  const tasks = [];
  let behavior = "success";
  let rejectRefresh = false;
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const project = path.includes("/beta/") ? "beta" : "alpha";
    const account = request.headers().authorization;
    if (path.endsWith("/events")) {
      await route.fulfill({ status: 403, body: "" });
      return;
    }
    if (path.endsWith("/tasks") && request.method() === "POST") {
      const title = request.postDataJSON().title;
      writes.push({ title, project, account });
      let result = behavior;
      if (behavior === "hold") {
        result = await new Promise((resolve) => { releaseCreate = resolve; });
      }
      if (result === "reject") {
        await route.fulfill({ status: 500, json: { error: "fixture failure" } });
        return;
      }
      if (result === "network") {
        await route.abort("failed");
        return;
      }
      const task = { id: tasks.length + 1, title, project, account, status: "backlog", priority: "medium" };
      tasks.push(task);
      if (result === "accepted-network") {
        await route.abort("failed");
        return;
      }
      await route.fulfill({ json: task });
      return;
    }
    if (path.endsWith("/tasks") && rejectRefresh) {
      await route.fulfill({ status: 500, json: { error: "refresh failure" } });
      return;
    }
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Alpha" }, { id: 2, slug: "beta", name: "Beta" }];
    }
    if (path.endsWith("/tasks")) {
      data = tasks.filter((task) => task.project === project && task.account === account);
    }
    if (path.endsWith("/role")) {
      data = { role: "owner" };
    }
    await route.fulfill({ json: data });
  });
  await page.goto(url);
  const input = page.getByPlaceholder("Новая задача...");
  const add = page.getByRole("button", { name: "Добавить", exact: true });
  const pending = page.getByRole("status").filter({ hasText: "Создаём задачу" });
  const failed = page.getByRole("alert").filter({ hasText: "Не удалось подтвердить создание задачи" });
  const switchProject = async (project) => {
    await page.locator("header select").first().selectOption(project);
    await input.waitFor();
  };
  const switchAccount = async (token) => {
    await page.evaluate((value) => {
      localStorage.setItem("akhflow.token", value);
      window.dispatchEvent(new StorageEvent("storage", { key: "akhflow.token", storageArea: localStorage }));
    }, token);
    await page.waitForFunction(() => document.querySelector('input[placeholder="Новая задача..."]'));
    await page.locator("header select").first().selectOption("alpha");
  };
  const holdCreate = async (title) => {
    behavior = "hold";
    releaseCreate = undefined;
    await input.fill(title);
    await input.press("Enter");
    await pending.waitFor();
    while (!releaseCreate) {
      await delay(10);
    }
  };

  await input.fill("   ");
  assert.equal(await add.isDisabled(), true);
  await input.press("Enter");
  assert.equal(writes.length, 0);
  await holdCreate("First request");
  await input.press("Enter");
  await input.press("Enter");
  assert.equal(await add.isDisabled(), true);
  await input.fill("Next draft");
  assert.equal(writes.length, 1, "repeated Enter duplicated creation");
  releaseCreate("success");
  await pending.waitFor({ state: "hidden" });
  assert.equal(await input.inputValue(), "Next draft", "success erased later input");
  await page.getByRole("button", { name: "First request", exact: true }).waitFor();
  behavior = "success";
  await add.click();
  await page.getByRole("button", { name: "Next draft", exact: true }).waitFor();
  assert.equal(await input.inputValue(), "");

  await holdCreate("Same value after editing");
  await input.fill("Temporary edit");
  await input.fill("Same value after editing");
  releaseCreate("success");
  await pending.waitFor({ state: "hidden" });
  assert.equal(await input.inputValue(), "Same value after editing", "matching text hid a later revision");
  await input.fill("");

  behavior = "reject";
  await input.fill("Retry failure");
  await add.click();
  await failed.waitFor();
  assert.equal(await input.inputValue(), "Retry failure");
  behavior = "network";
  await add.click();
  await pending.waitFor({ state: "hidden" });
  await failed.waitFor();
  assert.equal(await input.inputValue(), "Retry failure");
  behavior = "success";
  await add.click();
  await page.getByRole("button", { name: "Retry failure", exact: true }).waitFor();
  await failed.waitFor({ state: "hidden" });

  behavior = "accepted-network";
  await input.fill("Created despite connection loss");
  await add.click();
  await failed.waitFor();
  assert.ok((await failed.innerText()).includes("Проверь список перед повторной отправкой"));
  assert.equal(await input.inputValue(), "Created despite connection loss");
  const writesBeforeCheck = writes.length;
  await page.getByRole("button", { name: "Проверить список задач", exact: true }).click();
  await page.getByRole("button", { name: "Created despite connection loss", exact: true }).waitFor();
  assert.equal(writes.length, writesBeforeCheck, "checking uncertain creation repeated POST");
  assert.equal(await input.inputValue(), "Created despite connection loss");

  await holdCreate("Project request");
  await input.fill("Alpha draft");
  await switchProject("beta");
  assert.equal(await input.inputValue(), "");
  await input.fill("Beta draft");
  releaseCreate("success");
  await delay(100);
  assert.equal(await input.inputValue(), "Beta draft", "old project response erased current draft");
  assert.equal(await page.getByRole("button", { name: "Project request", exact: true }).count(), 0);
  await switchProject("alpha");
  assert.equal(await input.inputValue(), "Alpha draft", "project return lost subsequent input");
  await page.getByRole("button", { name: "Project request", exact: true }).waitFor();

  await holdCreate("Remount request");
  const writesBeforeRemount = writes.length;
  await switchProject("beta");
  await switchProject("alpha");
  await pending.waitFor();
  await input.press("Enter");
  assert.equal(writes.length, writesBeforeRemount, "remount bypassed pending guard");
  await input.fill("After remount");
  releaseCreate("success");
  await pending.waitFor({ state: "hidden" });
  assert.equal(await input.inputValue(), "After remount");
  await page.getByRole("button", { name: "Remount request", exact: true }).waitFor();

  await holdCreate("Failed account request");
  await input.fill("Account draft");
  await switchAccount("other-fixture-token");
  assert.equal(await input.inputValue(), "", "draft leaked across accounts");
  await input.fill("Other account draft");
  releaseCreate("reject");
  await delay(100);
  assert.equal(await input.inputValue(), "Other account draft");
  assert.equal(await failed.count(), 0, "old account failure appeared in current account");
  await switchAccount("fixture-token");
  assert.equal(await input.inputValue(), "Account draft");
  await failed.waitFor();
  assert.ok((await failed.innerText()).includes("Failed account request"));

  await holdCreate("Successful account request");
  await input.fill("Later account draft");
  await switchAccount("other-fixture-token");
  assert.equal(await input.inputValue(), "Other account draft");
  releaseCreate("success");
  await delay(100);
  assert.equal(await input.inputValue(), "Other account draft");
  await switchAccount("fixture-token");
  assert.equal(await input.inputValue(), "Later account draft");
  await page.getByRole("button", { name: "Successful account request", exact: true }).waitFor();

  behavior = "success";
  rejectRefresh = true;
  await input.fill("Created before refresh failure");
  await add.click();
  await page.getByRole("alert").filter({ hasText: "Задача создана, но список не обновился" }).waitFor();
  assert.equal(await input.inputValue(), "");
  const beforeRefreshRetry = writes.length;
  rejectRefresh = false;
  await page.getByRole("button", { name: "Обновить список", exact: true }).click();
  await page.getByRole("button", { name: "Created before refresh failure", exact: true }).waitFor();
  assert.equal(writes.length, beforeRefreshRetry, "refresh retry created a duplicate task");
  assert.deepEqual(errors, [], "creation produced an unhandled error");
  console.log("PASS: creation keeps later drafts, blocks duplicate submits, handles failures and isolates workspace responses");
} finally {
  releaseCreate?.("success");
  if (browser) {
    await browser.close();
  }
  server.kill();
}
