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
const first = { id: 1, title: "Задача А", description: "", status: "backlog", priority: "medium" };
const second = { id: 2, title: "Задача Б", description: "", status: "todo", priority: "medium" };
const comments = [];
const writes = [];
const pageErrors = [];
let mode = "delayed";
let release;
let started;
let holdNextDetail = false;
let releaseDetail;
let detailStarted;
let detailFinished;
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
  page.on("pageerror", (error) => pageErrors.push(error.message));
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "fixture-token");
    localStorage.setItem("akhflow.project", "alpha");
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Альфа" }, { id: 2, slug: "beta", name: "Бета" }];
    } else if (path === "/api/projects/alpha/tasks") {
      data = [first, second];
    } else if (path === "/api/tasks/1/comments") {
      writes.push(request.postDataJSON().body);
      if (mode === "delayed" || mode === "delayed-failure") {
        started();
        await new Promise((resolve) => { release = resolve; });
      }
      if (mode === "failure" || mode === "delayed-failure") {
        await route.fulfill({ status: 500, json: { error: "failure" } });
        return;
      }
      comments.push({ id: writes.length, body: request.postDataJSON().body, created_at: "2026-01-01T00:00:00Z" });
    } else if (path === "/api/tasks/1") {
      if (mode === "refresh-failure") {
        await route.fulfill({ status: 500, json: { error: "failure" } });
        return;
      }
      data = { task: first, dependencies: [second], comments, blocks: [], history: [] };
      if (holdNextDetail) {
        holdNextDetail = false;
        const snapshot = structuredClone(data);
        detailStarted();
        await new Promise((resolve) => { releaseDetail = resolve; });
        await route.fulfill({ status: 200, json: snapshot }).catch(() => {});
        detailFinished();
        return;
      }
    } else if (path === "/api/tasks/2") {
      data = { task: second, dependencies: [first], comments: [], blocks: [], history: [] };
    }
    await route.fulfill({ status: 200, json: data }).catch(() => {});
  });
  await page.goto(url);
  await page.getByRole("button", { name: "Задача А", exact: true }).click();
  const input = page.getByPlaceholder("Комментарий...");
  const send = page.getByRole("button", { name: "Отправить комментарий" });
  await input.fill("Первый");
  const received = new Promise((resolve) => { started = resolve; });
  await input.press("Enter");
  await received;
  await input.fill("Второй");
  await input.press("Enter");
  assert.equal(await send.isDisabled(), true);
  await page.getByRole("button", { name: "#2 Задача Б" }).click();
  await page.getByRole("button", { name: "Закрыть карточку задачи" }).click();
  assert.equal(await input.inputValue(), "Второй");
  assert.deepEqual(writes, ["Первый"]);
  const chooseProject = async (name) => {
    await page.evaluate((value) => {
      const select = [...document.querySelectorAll("select")].find((item) => {
        return [...item.options].some((option) => option.value === value);
      });
      select.value = value;
      select.dispatchEvent(new Event("change", { bubbles: true }));
    }, name);
  };
  await chooseProject("beta");
  await page.getByText("Задача #1", { exact: true }).waitFor({ state: "hidden" });
  await chooseProject("alpha");
  await page.getByRole("button", { name: "Задача А", exact: true }).click();
  assert.equal(await input.inputValue(), "Второй");
  await input.fill("Изменённый черновик");
  await input.fill("Второй");
  await input.press("Enter");
  assert.equal(await send.isDisabled(), true, "remount allowed a duplicate pending POST");
  assert.deepEqual(writes, ["Первый"]);
  mode = "success";
  release();
  await page.getByText("Первый", { exact: true }).waitFor();
  assert.equal(await input.inputValue(), "Второй");
  await page.getByRole("button", { name: "#2 Задача Б" }).click();
  await page.getByText("Задача #2", { exact: true }).waitFor();
  assert.equal(await input.inputValue(), "");
  await input.fill("Черновик второй задачи");
  await page.getByRole("button", { name: "#1 Задача А" }).click();
  await page.getByText("Задача #1", { exact: true }).waitFor();
  assert.equal(await input.inputValue(), "Второй");
  await page.getByRole("button", { name: "#2 Задача Б" }).click();
  await page.getByText("Задача #2", { exact: true }).waitFor();
  assert.equal(await input.inputValue(), "Черновик второй задачи");
  await page.getByRole("button", { name: "#1 Задача А" }).click();
  await page.getByText("Задача #1", { exact: true }).waitFor();
  mode = "failure";
  await send.click();
  await page.getByRole("alert").getByText("Не удалось подтвердить отправку. Проверь список комментариев перед повтором.").waitFor();
  assert.equal(await input.inputValue(), "Второй");
  mode = "success";
  await send.click();
  await page.getByText("Второй", { exact: true }).waitFor();
  assert.equal(await input.inputValue(), "");
  await input.fill("Третий");
  mode = "refresh-failure";
  await send.click();
  await page.getByRole("alert").getByText("Комментарий отправлен, но обновить список не удалось.").waitFor();
  assert.equal(await input.inputValue(), "");
  assert.deepEqual(writes, ["Первый", "Второй", "Второй", "Третий"]);
  mode = "delayed-failure";
  await input.fill("Запрос со сбоем");
  const failedRequest = new Promise((resolve) => { started = resolve; });
  await input.press("Enter");
  await failedRequest;
  await chooseProject("beta");
  await page.getByText("Задача #1", { exact: true }).waitFor({ state: "hidden" });
  await chooseProject("alpha");
  await page.getByRole("button", { name: "Задача А", exact: true }).click();
  await input.fill("Текст после возврата");
  release();
  await page.getByRole("alert").getByText("Не удалось подтвердить отправку. Проверь список комментариев перед повтором.").waitFor();
  assert.equal(await input.inputValue(), "Текст после возврата", "late failure lost the new draft");
  mode = "success";
  await input.fill("Первая быстрая отправка");
  holdNextDetail = true;
  const firstDetail = new Promise((resolve) => { detailStarted = resolve; });
  await send.click();
  await firstDetail;
  await input.fill("Вторая быстрая отправка");
  await send.click();
  await page.getByText("Вторая быстрая отправка", { exact: true }).waitFor();
  const staleResponse = new Promise((resolve) => { detailFinished = resolve; });
  releaseDetail();
  await staleResponse;
  await page.waitForTimeout(100);
  assert.equal(await page.getByText("Вторая быстрая отправка", { exact: true }).count(), 1, "older refresh hid the new comment");
  assert.deepEqual(pageErrors, []);
  console.log("PASS: comments preserve new input, prevent duplicate pending writes and isolate drafts");
} finally {
  if (release) {
    release();
  }
  if (releaseDetail) {
    releaseDetail();
  }
  if (browser) {
    await browser.close();
  }
  server.kill();
}
