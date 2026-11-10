import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { setTimeout as delay } from "node:timers/promises";
import { pathToFileURL } from "node:url";

const moduleName = process.env.PLAYWRIGHT_MODULE;
const playwright = await import(moduleName ? pathToFileURL(moduleName).href : "playwright");
const probe = createServer();
await new Promise((resolve) => probe.listen(0, "127.0.0.1", resolve));
const port = probe.address().port;
await new Promise((resolve) => probe.close(resolve));
const url = `http://127.0.0.1:${port}`;
const server = spawn("./node_modules/.bin/vite", ["--host", "127.0.0.1", "--port", String(port), "--strictPort"], {
  cwd: new URL("../web", import.meta.url), stdio: "ignore",
});
const preferences = new Map();
let failSave = false;
let failLoad = false;
let browser;
try {
  let ready = false;
  for (let attempt = 0; attempt < 100; attempt++) {
    try { ready = (await fetch(url)).ok; } catch { await delay(100); }
    if (ready) break;
  }
  assert.ok(ready, "Vite did not start");
  browser = await playwright.chromium.launch({ executablePath: "/usr/bin/google-chrome", headless: true });
  const page = await browser.newPage();
  await page.addInitScript(() => {
    if (!localStorage.getItem("akhflow.token")) localStorage.setItem("akhflow.token", "user-a");
    if (!localStorage.getItem("akhflow.project")) localStorage.setItem("akhflow.project", "alpha");
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let data = [];
    if (path === "/api/projects") data = [{ id: 1, slug: "alpha", name: "Альфа" }, { id: 2, slug: "beta", name: "Бета" }];
    else if (path.endsWith("/dashboard-preferences")) {
      const project = path.split("/")[3];
      const key = `${request.headers().authorization}:${project}`;
      if (request.method() === "GET" && failLoad) { await route.fulfill({ status: 500, json: { error: "fixture" } }); return; }
      if (request.method() === "PUT") {
        if (failSave) { await route.fulfill({ status: 500, json: { error: "fixture" } }); return; }
        preferences.set(key, request.postDataJSON().hidden_widgets);
      }
      data = { hidden_widgets: preferences.get(key) ?? [] };
    } else if (path.endsWith("/tasks")) data = [{ id: 1, title: "Задача", status: "todo", priority: "medium" }];
    await route.fulfill({ status: 200, json: data });
  });
  await page.goto(url);
  await page.getByRole("button", { name: "Сводка", exact: true }).click();
  let dashboard = page.getByRole("region", { name: "Сводка проекта" });
  const statuses = () => dashboard.getByRole("checkbox", { name: "По статусам" });
  await statuses().waitFor();
  await statuses().uncheck();
  await page.waitForFunction(() => !document.querySelector('fieldset[disabled]'));
  assert.equal(await dashboard.getByText("По статусам", { exact: true }).count(), 1);
  await page.reload();
  await page.getByRole("button", { name: "Сводка", exact: true }).click();
  dashboard = page.getByRole("region", { name: "Сводка проекта" });
  await statuses().waitFor();
  await page.waitForFunction(() => !document.querySelector('fieldset[disabled]'));
  assert.equal(await statuses().isChecked(), false);
  await page.locator("header select").first().selectOption("beta");
  await page.waitForFunction(() => !document.querySelector('fieldset[disabled]'));
  assert.equal(await statuses().isChecked(), true);
  for (const label of ["По статусам", "По приоритетам", "По исполнителям", "Прогресс эпиков"]) {
    await dashboard.getByRole("checkbox", { name: label }).uncheck();
    await page.waitForFunction(() => !document.querySelector('fieldset[disabled]'));
    assert.equal(await dashboard.getByText(label, { exact: true }).count(), 1);
  }
  failLoad = true;
  await page.reload();
  await page.getByRole("button", { name: "Сводка", exact: true }).click();
  await dashboard.getByText("Не удалось загрузить настройки виджетов.", { exact: false }).waitFor();
  failLoad = false;
  await dashboard.getByRole("button", { name: "Повторить" }).click();
  await page.waitForFunction(() => !document.querySelector('fieldset[disabled]'));
  assert.equal(await statuses().isChecked(), false);
  await page.locator("header select").first().selectOption("alpha");
  await page.waitForFunction(() => !document.querySelector('fieldset[disabled]'));
  assert.equal(await statuses().isChecked(), false);
  failSave = true;
  await statuses().click();
  await dashboard.getByRole("alert").getByText("Не удалось сохранить настройки виджетов.").waitFor();
  assert.equal(await statuses().isChecked(), false);
  failSave = false;
  await page.evaluate(() => localStorage.setItem("akhflow.token", "user-b"));
  await page.reload();
  await page.getByRole("button", { name: "Сводка", exact: true }).click();
  await page.waitForFunction(() => !document.querySelector('fieldset[disabled]'));
  assert.equal(await statuses().isChecked(), true);
  console.log("PASS: widget visibility persists per user and project and save failures roll back");
} finally {
  if (browser) await browser.close();
  server.kill();
}
