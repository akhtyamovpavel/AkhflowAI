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
let releasePost = () => {};
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
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "account-a");
    localStorage.setItem("akhflow.project", "alpha");
  });

  const posts = [];
  const projectLoads = [];
  let mode = "fail";
  let postStarted = () => {};
  let waitingForPost = new Promise((resolve) => { postStarted = resolve; });
  let pendingPost = new Promise((resolve) => { releasePost = resolve; });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let data = [];
    if (path === "/api/projects") {
      projectLoads.push(request.headers().authorization);
      data = [{ id: 1, slug: "alpha", name: "Alpha" }, { id: 2, slug: "beta", name: "Beta" }];
    } else if (path.endsWith("/role")) {
      data = { role: "owner" };
    } else if (path.endsWith("/events")) {
      await route.fulfill({ status: 403, body: "" }).catch(() => {});
      return;
    } else if (path.includes("/agent/usage")) {
      data = { requests: 0, replies: 0, tool_calls: 0 };
    } else if (path.endsWith("/chats")) {
      data = [{ id: 1, title: "Первый чат" }, { id: 2, title: "Второй чат" }];
    } else if (path.match(/^\/api\/chats\/\d+\/runs$/) && request.method() === "POST") {
      posts.push({ path, body: request.postDataJSON() });
      postStarted();
      await pendingPost;
      if (mode === "fail") {
        await route.fulfill({ status: 503, json: { error: "unavailable" } }).catch(() => {});
        return;
      }
      const chatId = Number(path.split("/")[3]);
      data = { id: 1, chat_id: chatId, request_key: request.headers()["idempotency-key"], status: "queued" };
      await route.fulfill({ status: 202, json: data }).catch(() => {});
      return;
    }
    await route.fulfill({ json: data }).catch(() => {});
  });
  const switchAccount = async (token) => {
    const account = `Bearer ${token}`;
    const previous = projectLoads.filter((value) => value === account).length;
    await page.evaluate((value) => {
      localStorage.setItem("akhflow.token", value);
      window.dispatchEvent(new StorageEvent("storage", { key: "akhflow.token", storageArea: localStorage }));
    }, token);
    const deadline = Date.now() + 5000;
    while (projectLoads.filter((value) => value === account).length <= previous && Date.now() < deadline) {
      await delay(10);
    }
    assert.ok(projectLoads.filter((value) => value === account).length > previous);
  };

  await page.goto(url, { timeout: 20000 });
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  const first = page.getByRole("button", { name: "Первый чат" });
  const second = page.getByRole("button", { name: "Второй чат" });
  const input = page.getByPlaceholder("Скажи агенту, что сделать...");
  await input.waitFor();
  await page.getByText("Попробуй:", { exact: false }).waitFor();
  await input.fill("Черновик первого");
  await second.click();
  await page.getByText("Попробуй:", { exact: false }).waitFor();
  assert.equal(await input.inputValue(), "", "first chat draft leaked into second chat");
  await input.fill("Черновик второго");
  await first.click();
  assert.equal(await input.inputValue(), "Черновик первого");
  await input.press("Enter");
  await waitingForPost;
  assert.equal(await input.inputValue(), "", "submitted text remained ready to resend");
  await input.fill("Новый текст после ошибки");
  releasePost();
  await page.getByRole("alert").filter({ hasText: "Результат отправки неизвестен" }).waitFor();
  assert.equal(await input.inputValue(), "Новый текст после ошибки", "failed send erased newer input");
  await page.getByText("Черновик первого", { exact: true }).first().waitFor();
  assert.equal(posts[0].path, "/api/chats/1/runs");
  await input.press("Enter");
  assert.equal(posts.length, 1, "uncertain send must block the next POST");
  await page.getByRole("button", { name: "Проверить запуск" }).click();
  mode = "success";
  await page.getByRole("button", { name: "Повторить проверенную отправку" }).click();
  await page.getByRole("alert").filter({ hasText: "Результат отправки неизвестен" }).waitFor({ state: "hidden" });
  assert.equal(await input.inputValue(), "Новый текст после ошибки", "acknowledgement erased newer input");

  mode = "hold";
  waitingForPost = new Promise((resolve) => { postStarted = resolve; });
  pendingPost = new Promise((resolve) => { releasePost = resolve; });
  await input.press("Enter");
  await waitingForPost;
  await input.fill("Новый текст после отправки");
  await second.click();
  assert.equal(await input.inputValue(), "Черновик второго");
  await page.locator("header select").first().selectOption("beta");
  await input.waitFor();
  assert.equal(await input.inputValue(), "", "draft leaked into another project");
  await input.fill("Черновик другого проекта");
  await page.locator("header select").first().selectOption("alpha");
  await input.waitFor();
  assert.equal(await input.inputValue(), "Новый текст после отправки");
  await input.press("Enter");
  assert.equal(posts.length, 3, "pending send was repeated after a project switch");
  releasePost();
  await page.getByRole("alert").filter({ hasText: "Результат отправки неизвестен" }).waitFor();
  assert.equal(await input.inputValue(), "Новый текст после отправки", "late success erased a newer draft");

  await page.locator("header select").first().selectOption("beta");
  await input.waitFor();
  assert.equal(await input.inputValue(), "Черновик другого проекта");
  await page.locator("header select").first().selectOption("alpha");
  await input.waitFor();
  assert.equal(await input.inputValue(), "Новый текст после отправки");

  await switchAccount("account-b");
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await input.waitFor();
  assert.equal(await input.inputValue(), "", "draft leaked into another account");
  await input.fill("Черновик другого аккаунта");
  await switchAccount("account-a");
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await input.waitFor();
  assert.equal(await input.inputValue(), "Новый текст после отправки");
  assert.equal(posts.length, 3);
  console.log("PASS: chat drafts stay scoped and survive failed or late sends");
} finally {
  releasePost();
  if (browser) {
    await browser.close();
  }
  server.kill();
}
