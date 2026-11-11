import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { setTimeout as delay } from "node:timers/promises";
import { pathToFileURL } from "node:url";

const defaultModule = new URL("../web/node_modules/playwright-core/index.mjs", import.meta.url).href;
const playwright = await import(process.env.PLAYWRIGHT_MODULE ? pathToFileURL(process.env.PLAYWRIGHT_MODULE).href : defaultModule);
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
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      if ((await fetch(url)).ok) break;
    } catch {
      await delay(100);
    }
  }
  browser = await playwright.chromium.launch({ executablePath: "/usr/bin/google-chrome", headless: true });
  const page = await browser.newPage();
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "fixture");
    localStorage.setItem("akhflow.project", "alpha");
  });
  let role = "owner";
  let saved = { instructions: "Исходные правила", version: 1, updated_at: null };
  let fail = false;
  const writes = [];
  let hold = false;
  let release;
  let started;
  let pending = new Promise((resolve) => { started = resolve; });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let data = [];
    if (path === "/api/projects") data = [{ id: 1, slug: "alpha", name: "Alpha" }];
    if (path === "/api/projects/alpha/role") data = { role };
    if (path === "/api/projects/alpha/agent/instructions") {
      if (request.method() === "PUT") {
        const input = request.postDataJSON();
        writes.push(input);
        if (hold) {
          started();
          await new Promise((resolve) => { release = resolve; });
        }
        if (fail) {
          await route.fulfill({ status: 503, json: {} });
          return;
        }
        if (input.version !== saved.version) {
          await route.fulfill({ status: 409, json: {} });
          return;
        }
        saved = { instructions: input.instructions, version: saved.version + 1, updated_at: null };
      }
      data = saved;
    }
    await route.fulfill({ status: 200, json: data }).catch(() => {});
  });
  await page.goto(url);
  await page.getByRole("button", { name: "Инструкции агента" }).click();
  const dialog = page.getByRole("dialog", { name: "Инструкции агента" });
  const input = dialog.getByLabel("Текст инструкций");
  await input.waitFor();
  await page.waitForFunction(() => document.querySelector("textarea")?.value === "Исходные правила");
  await input.fill("Мой черновик");
  saved = { instructions: "Параллельные правила", version: 2, updated_at: null };
  await dialog.getByRole("button", { name: "Сохранить" }).click();
  await dialog.getByText("Инструкции изменились", { exact: false }).waitFor();
  assert.equal(await input.inputValue(), "Мой черновик");
  assert.equal(await dialog.getByRole("button", { name: "Сохранить" }).isDisabled(), true);
  assert.equal(writes.length, 1, "conflict must block retry before explicit refresh");
  await dialog.getByRole("button", { name: "Загрузить актуальную версию" }).click();
  await dialog.getByText("Параллельные правила").waitFor();
  assert.equal(await input.inputValue(), "Мой черновик");
  assert.equal(await dialog.getByRole("button", { name: "Сохранить" }).isDisabled(), true);
  await dialog.getByRole("button", { name: "Использовать версию для черновика" }).click();
  fail = true;
  await dialog.getByRole("button", { name: "Сохранить" }).click();
  await dialog.getByText("Не удалось подтвердить сохранение", { exact: false }).waitFor();
  assert.equal(await input.inputValue(), "Мой черновик");
  fail = false;
  hold = true;
  await dialog.getByRole("button", { name: "Сохранить" }).click();
  await pending;
  assert.equal(await input.getAttribute("readonly"), "", "editing must be blocked during save");
  await input.focus();
  await page.keyboard.type(" не сохраняется");
  assert.equal(await input.inputValue(), "Мой черновик");
  hold = false;
  release();
  await dialog.getByText("Инструкции сохранены.").waitFor();
  assert.equal(writes.at(-1).version, 2);
  assert.equal(saved.instructions, "Мой черновик");
  await dialog.getByRole("button", { name: "Закрыть" }).click();
  role = "viewer";
  await page.reload();
  await page.getByRole("button", { name: "Инструкции агента" }).click();
  await page.waitForFunction(() => document.querySelector("textarea")?.value === "Мой черновик");
  assert.equal(await input.getAttribute("readonly"), "");
  assert.equal(await dialog.getByRole("button", { name: "Сохранить" }).count(), 0);
  await page.keyboard.press("Escape");
  assert.equal(await dialog.count(), 0);
  console.log("PASS: instruction roles, conflicts, draft preservation and dialog");
} finally {
  if (browser) await browser.close();
  server.kill();
}
