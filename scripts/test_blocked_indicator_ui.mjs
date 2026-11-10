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
      data = [{ id: 1, slug: "alpha", name: "Альфа" }];
    } else if (path === "/api/projects/alpha/epics") {
      data = [{ id: 11, title: "Первый" }, { id: 12, title: "Второй" }];
    } else if (path === "/api/projects/alpha/tasks") {
      data = [
        { id: 1, title: "Ожидающая", status: "todo", priority: "medium", epic_id: 11, depends_on: [2, 3] },
        { id: 2, title: "Незавершённая", status: "todo", priority: "medium", epic_id: 12 },
        { id: 3, title: "Завершённая", status: "done", priority: "medium", epic_id: 12 },
        { id: 4, title: "Разблокированная", status: "todo", priority: "medium", epic_id: 11, depends_on: [3] },
      ];
    }
    await route.fulfill({ status: 200, json: data });
  });
  await page.goto(url);
  const waitingCard = page.getByRole("button", { name: "Ожидающая", exact: true }).locator("..");
  const unblockedCard = page.getByRole("button", { name: "Разблокированная", exact: true }).locator("..");
  await waitingCard.getByText("ждёт: 1").waitFor();
  assert.equal(await unblockedCard.getByText(/ждёт:/).count(), 0);

  const epicFilter = page.locator("header select").nth(1);
  await epicFilter.selectOption("11");
  assert.equal(await page.getByRole("button", { name: "Незавершённая", exact: true }).count(), 0);
  await waitingCard.getByText("ждёт: 1").waitFor();
  assert.equal(await unblockedCard.getByText(/ждёт:/).count(), 0);

  await epicFilter.selectOption("all");
  await page.getByRole("button", { name: "Незавершённая", exact: true }).waitFor();
  await waitingCard.getByText("ждёт: 1").waitFor();
  console.log("PASS: dependency indicator across epic filters and completed dependencies");
} finally {
  if (browser) {
    await browser.close();
  }
  server.kill();
}
