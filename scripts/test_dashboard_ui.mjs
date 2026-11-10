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
  let failBeta = false;
  let alphaTaskDone = false;
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Альфа" }, { id: 2, slug: "beta", name: "Бета" }];
    } else if (path.endsWith("/dashboard-preferences")) {
      data = { hidden_widgets: [] };
    } else if (path === "/api/projects/alpha/tasks") {
      data = [
        { id: 1, title: "Первая", status: alphaTaskDone ? "done" : "backlog", priority: "high", assignee_id: 1, epic_id: 7 },
        { id: 2, title: "Вторая", status: "done", priority: "low", assignee_id: null, epic_id: 7 },
      ];
    } else if (path === "/api/projects/alpha/epics") {
      data = [{ id: 7, title: "Эпик с задачами" }, { id: 8, title: "Пустой эпик" }];
    } else if (path === "/api/projects/alpha/members") {
      data = [{ id: 1, display_name: "Анна", email: "anna@example.com" }];
    } else if (path === "/api/projects/beta/tasks") {
      if (failBeta) {
        await route.fulfill({ status: 500, json: { error: "fixture" } });
        return;
      }
      data = [{ id: 3, title: "Третья", status: "todo", priority: "medium", assignee_id: 2 }];
    } else if (path === "/api/projects/beta/members") {
      data = [{ id: 2, display_name: "Борис", email: "boris@example.com" }];
    }
    await route.fulfill({ status: 200, json: data });
  });
  await page.goto(url);
  await page.getByRole("button", { name: "Сводка", exact: true }).click();
  const dashboard = page.getByRole("region", { name: "Сводка проекта" });
  await dashboard.getByText("Всего задач:").waitFor();
  assert.match(await dashboard.innerText(), /Всего задач: 2/);
  assert.match(await dashboard.innerText(), /Анна\s+1/);
  assert.match(await dashboard.innerText(), /Без исполнителя\s+1/);
  const epicBar = dashboard.getByRole("progressbar", { name: "Прогресс эпика Эпик с задачами" });
  const emptyBar = dashboard.getByRole("progressbar", { name: "Прогресс эпика Пустой эпик" });
  assert.equal(await epicBar.getAttribute("value"), "50");
  assert.equal(await epicBar.getAttribute("aria-valuetext"), "1 из 2, 50%");
  assert.equal(await emptyBar.getAttribute("aria-valuetext"), "0 из 0, 0%");
  await page.locator("header select").nth(1).selectOption("none");
  assert.equal(await epicBar.getAttribute("value"), "50");
  alphaTaskDone = true;
  await dashboard.getByRole("button", { name: "Обновить" }).click();
  await page.waitForFunction(() => {
    const bar = document.querySelector('[aria-label="Прогресс эпика Эпик с задачами"]');
    return bar?.getAttribute("value") === "100";
  });
  assert.equal(await epicBar.getAttribute("aria-valuetext"), "2 из 2, 100%");
  await page.locator("header select").first().selectOption("beta");
  await page.getByRole("region", { name: "Сводка проекта" }).getByText("Борис").waitFor();
  assert.match(await dashboard.innerText(), /Всего задач: 1/);
  assert.doesNotMatch(await dashboard.innerText(), /Анна/);
  assert.equal(await dashboard.getByRole("progressbar").count(), 0);
  assert.match(await dashboard.innerText(), /В проекте пока нет эпиков/);
  await page.locator("header select").nth(1).selectOption("none");
  assert.match(await dashboard.innerText(), /Всего задач: 1/);
  failBeta = true;
  await dashboard.getByRole("button", { name: "Обновить" }).click();
  await dashboard.getByRole("alert").waitFor();
  failBeta = false;
  await dashboard.getByRole("button", { name: "Обновить" }).click();
  await dashboard.getByText("Борис").waitFor();
  console.log("PASS: dashboard rendering, project switch, epic filter and error retry");
} finally {
  if (browser) {
    await browser.close();
  }
  server.kill();
}
