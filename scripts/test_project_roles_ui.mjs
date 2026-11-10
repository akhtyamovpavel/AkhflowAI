import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { setTimeout as delay } from "node:timers/promises";
import { pathToFileURL } from "node:url";

const moduleName = process.env.PLAYWRIGHT_MODULE;
const modulePath = moduleName ? pathToFileURL(moduleName).href : "playwright";
const playwright = await import(modulePath);
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
  const context = await browser.newContext();
  let role = "owner";
  let saved;
  await context.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    let data = [];
    if (path === "/api/projects") data = [{ id: 1, slug: "alpha", name: "Alpha" }];
    if (path === "/api/projects/alpha/role") data = { role };
    if (path === "/api/projects/alpha/tasks" && route.request().method() === "POST" && role === "viewer") {
      await route.fulfill({ status: 403, json: { error: "access denied" } });
      return;
    }
    if (path === "/api/projects/alpha/members") {
      if (route.request().method() === "PUT") {
        saved = route.request().postDataJSON();
        data = { status: "updated" };
      } else {
        data = [{ id: 1, email: "owner@example.invalid", display_name: "Owner", role: "owner" }];
      }
    }
    await route.fulfill({ status: 200, json: data });
  });
  await context.addInitScript(() => localStorage.setItem("akhflow.token", "fixture-token"));
  const page = await context.newPage();
  const duplicateKeyWarnings = [];
  page.on("console", (message) => {
    if (message.text().includes("same key")) duplicateKeyWarnings.push(message.text());
  });
  const assertSinglePanels = async () => {
    assert.equal(await page.getByRole("region", { name: "Права проекта" }).count(), 1);
    assert.equal(await page.getByRole("region", { name: "Поиск по проекту" }).count(), 1);
    assert.equal(await page.getByLabel("Поиск по задачам и эпикам").count(), 1);
  };
  await page.goto(url);
  await page.getByRole("button", { name: "Участники и роли" }).click();
  await assertSinglePanels();
  await page.getByLabel("Email зарегистрированного пользователя").fill("editor@example.invalid");
  await page.getByLabel("Роль", { exact: true }).selectOption("editor");
  await page.getByRole("button", { name: "Сохранить права" }).click();
  await page.getByText("Права участника сохранены").waitFor();
  assert.deepEqual(saved, { email: "editor@example.invalid", role: "editor" });
  role = "viewer";
  await page.reload();
  await page.getByText("Доступ к проекту: только просмотр.").waitFor();
  assert.equal(await page.getByRole("button", { name: "Участники и роли" }).count(), 0);
  await assertSinglePanels();
  await page.getByPlaceholder("Новая задача...").fill("Запрещённая задача");
  await page.getByRole("button", { name: "Добавить", exact: true }).click();
  await page.getByRole("alert").getByText("Недостаточно прав для выполнения действия.", { exact: false }).waitFor();
  await assertSinglePanels();
  await page.getByRole("button", { name: "Закрыть", exact: true }).click();
  await assertSinglePanels();
  assert.deepEqual(duplicateKeyWarnings, []);
  console.log("PASS: owner can manage roles and viewer sees read-only status");
} finally {
  if (browser) await browser.close();
  server.kill();
}
