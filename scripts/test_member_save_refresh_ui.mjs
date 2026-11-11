import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { setTimeout as delay } from "node:timers/promises";
import { pathToFileURL } from "node:url";

const modulePath = process.env.PLAYWRIGHT_MODULE;
const defaultModule = new URL("../web/node_modules/playwright-core/index.mjs", import.meta.url).href;
const playwright = await import(modulePath ? pathToFileURL(modulePath).href : defaultModule);
const probe = createServer();
await new Promise((resolve) => probe.listen(0, "127.0.0.1", resolve));
const port = probe.address().port;
await new Promise((resolve) => probe.close(resolve));
const url = `http://127.0.0.1:${port}`;
const options = { cwd: new URL("../web", import.meta.url), stdio: "ignore" };
const server = spawn("./node_modules/.bin/vite", ["--host", "127.0.0.1", "--port", String(port), "--strictPort"], options);
let browser;

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
  page.setDefaultTimeout(8000);
  page.setDefaultNavigationTimeout(15000);
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "fixture-token");
    const original = window.fetch.bind(window);
    window.holdMemberPut = "";
    window.fetch = (input, options = {}) => {
      if (window.holdMemberPut && String(input).endsWith("/members") && options.method === "PUT") {
        return new Promise((resolve) => {
          window.releaseMemberPut = () => {
            const status = window.holdMemberPut === "success" ? 200 : 401;
            resolve(new Response("{}", { status }));
          };
        });
      }
      return original(input, options);
    };
  });
  let putCount = 0;
  let failures = 0;
  let mode = "success";
  let role = "viewer";
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path.endsWith("/events")) {
      await route.fulfill({ status: 403, body: "" });
      return;
    }
    if (path.endsWith("/members") && request.method() === "PUT") {
      putCount++;
      const payload = request.postDataJSON();
      if (mode !== "failed") role = payload.role;
      if (mode === "unknown") {
        await route.abort("failed");
        return;
      }
      if (mode === "failed") {
        await route.fulfill({ status: 503, json: { error: "unavailable" } });
        return;
      }
      failures = 1;
      await route.fulfill({ json: { status: "updated" } });
      return;
    }
    if (path.endsWith("/members") && failures > 0) {
      failures--;
      await route.fulfill({ status: 503, json: { error: "unavailable" } });
      return;
    }
    let data = [];
    if (path === "/api/projects") data = [{ id: 1, slug: "alpha", name: "Alpha" }];
    if (path.endsWith("/role")) data = { role: "owner" };
    if (path.endsWith("/members")) {
      data = [{ id: 2, email: "editor@example.invalid", display_name: "Editor", role }];
    }
    await route.fulfill({ json: data });
  });
  await page.goto(url);
  await page.getByRole("button", { name: "Участники и роли" }).click();
  const email = page.getByLabel("Email зарегистрированного пользователя");
  const select = page.getByLabel("Роль", { exact: true });
  const save = page.getByRole("button", { name: "Сохранить права" });
  const retry = page.getByRole("button", { name: "Проверить список участников" });
  await email.fill("editor@example.invalid");
  await select.selectOption("editor");
  await save.click();
  await page.getByText("Права сохранены, но не удалось обновить список участников.").waitFor();
  assert.equal(putCount, 1);
  assert.equal(await save.isDisabled(), true);
  await retry.click();
  await page.getByText("Права участника сохранены").waitFor();
  assert.equal(putCount, 1, "refresh must not repeat PUT");

  mode = "unknown";
  await email.fill("editor@example.invalid");
  await select.selectOption("viewer");
  await save.click();
  await page.getByText("Не удалось подтвердить сохранение прав. Проверь актуальный список участников.").waitFor();
  assert.equal(await save.isDisabled(), true);
  await retry.click();
  await page.getByText("Права участника сохранены").waitFor();
  assert.equal(putCount, 2, "verification must not repeat an uncertain PUT");

  mode = "failed";
  await email.fill("editor@example.invalid");
  await select.selectOption("editor");
  await save.click();
  await retry.click();
  await page.getByText("Сохранение не подтверждено. Проверь текущие права и повтори сохранение при необходимости.").waitFor();
  assert.equal(await email.inputValue(), "editor@example.invalid");
  assert.equal(await save.isDisabled(), false);
  assert.equal(putCount, 3);

  await page.evaluate(() => { window.holdMemberPut = "success"; });
  await save.click();
  await page.waitForFunction(() => typeof window.releaseMemberPut === "function");
  await select.selectOption("viewer");
  await page.evaluate(() => window.releaseMemberPut());
  await page.getByText("Права участника сохранены").waitFor();
  assert.equal(await email.inputValue(), "editor@example.invalid", "new role selection must preserve the email");

  await page.evaluate(() => {
    window.releaseMemberPut = undefined;
    window.holdMemberPut = "unauthorized";
  });
  await save.click();
  await page.waitForFunction(() => typeof window.releaseMemberPut === "function");
  await page.evaluate(() => {
    localStorage.setItem("akhflow.token", "fixture-new-account");
    window.dispatchEvent(new StorageEvent("storage", { key: "akhflow.token", storageArea: localStorage }));
  });
  await page.getByRole("button", { name: "Участники и роли" }).waitFor();
  await page.evaluate(() => window.releaseMemberPut());
  await delay(100);
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-new-account");
  assert.equal(await page.getByText("Не удалось подтвердить сохранение прав.", { exact: false }).count(), 0);
  console.log("PASS: member save and refresh outcomes are distinguished");
} finally {
  if (browser) await browser.close();
  server.kill();
}
