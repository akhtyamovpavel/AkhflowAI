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
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "fixture-token");
    localStorage.setItem("akhflow.project", "alpha");
  });
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Альфа" }, { id: 2, slug: "beta", name: "Бета" }];
    } else if (path === "/api/projects/alpha/tasks") {
      data = [{ id: 1, title: "Русская задача", status: "backlog", priority: "high", epic_id: 7 }];
    } else if (path === "/api/projects/alpha/epics") {
      data = [{ id: 7, title: "Русский эпик", status: "active" }];
    } else if (path === "/api/projects/beta/tasks") {
      await delay(500);
      data = [{ id: 2, title: "Другая задача", status: "todo", priority: "medium" }];
    } else if (path === "/api/projects/beta/epics") {
      await delay(500);
    } else if (path === "/api/tasks/1") {
      data = { task: { id: 1, title: "Русская задача", status: "backlog", priority: "high" } };
    }
    await route.fulfill({ status: 200, json: data });
  });
  await page.goto(url);
  await page.getByRole("button", { name: "Русская задача", exact: true }).waitFor();
  const search = page.getByRole("searchbox", { name: "Поиск по задачам и эпикам" });
  const results = page.getByRole("region", { name: "Поиск по проекту" });
  await search.fill("  РУССК  ");
  await results.getByText("Найдено: 2").waitFor();
  await search.press("Tab");
  await page.keyboard.press("Enter");
  await page.getByText("Задача #1").waitFor();
  await page.locator(".fixed.inset-0.z-50").click({ position: { x: 5, y: 5 } });
  await search.fill("несуществующая");
  await results.getByText("Ничего не найдено").waitFor();
  await search.press("Escape");
  assert.equal(await search.inputValue(), "");
  await search.fill("эпик");
  await results.getByRole("button", { name: "Русский эпик" }).click();
  assert.equal(await page.locator("header select").nth(1).inputValue(), "7");
  await search.fill("русск");
  await page.locator("header select").first().selectOption("beta");
  await search.fill("русск");
  await results.getByText("Загружаем данные проекта").waitFor();
  await results.getByText("Ничего не найдено").waitFor();
  await search.fill("другая");
  await results.getByRole("button", { name: "#2 Другая задача" }).waitFor();
  assert.equal(await results.getByRole("button", { name: "#1 Русская задача" }).count(), 0);
  console.log("PASS: search results, Cyrillic case folding, keyboard, empty state, epic selection and project isolation");
} finally {
  if (browser) {
    await browser.close();
  }
  server.kill();
}
