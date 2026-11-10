import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { setTimeout as delay } from "node:timers/promises";
import { pathToFileURL } from "node:url";

const moduleName = process.env.PLAYWRIGHT_MODULE;
const moduleUrl = moduleName ? pathToFileURL(moduleName).href : "playwright";
const playwright = await import(moduleUrl);
const probe = createServer();
await new Promise((resolve) => probe.listen(0, "127.0.0.1", resolve));
const port = probe.address().port;
await new Promise((resolve) => probe.close(resolve));
const url = `http://127.0.0.1:${port}`;
const webDirectory = new URL("../web", import.meta.url);
const server = spawn("./node_modules/.bin/vite", ["--host", "127.0.0.1", "--port", String(port), "--strictPort"], {
  cwd: webDirectory,
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

  const current = { id: 1, title: "Текущая", status: "todo", priority: "medium" };
  const prerequisite = { id: 2, title: "Предшественник", status: "todo", priority: "medium" };
  const dependent = { id: 3, title: "Последователь", status: "todo", priority: "medium" };
  let dependencies = [prerequisite];
  let blocks = [dependent];
  let failBlockedRemoval = true;
  const requests = [];
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (request.method() === "DELETE") {
      requests.push(path);
      if (path === "/api/tasks/1/dependencies/2") {
        dependencies = [];
        await route.fulfill({ status: 200, json: { status: "deleted" } });
        return;
      }
      if (path === "/api/tasks/3/dependencies/1" && failBlockedRemoval) {
        await route.fulfill({ status: 500, json: { error: "fixture error" } });
        return;
      }
      if (path === "/api/tasks/3/dependencies/1") {
        blocks = [];
        await route.fulfill({ status: 200, json: { status: "deleted" } });
        return;
      }
      await route.fulfill({ status: 404, json: { error: "unexpected dependency" } });
      return;
    }
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Альфа" }];
    } else if (path === "/api/projects/alpha/tasks") {
      data = [current, prerequisite, dependent];
    } else if (path === "/api/tasks/1") {
      data = { task: current, dependencies, blocks, comments: [], history: [] };
    }
    await route.fulfill({ status: 200, json: data });
  });

  await page.goto(url);
  await page.getByRole("button", { name: "Текущая", exact: true }).click();
  const prerequisiteRow = page.getByRole("button", { name: "#2 Предшественник" }).locator("..");
  await prerequisiteRow.locator("button").last().click();
  await page.getByText("Зависит от (0)").waitFor();
  assert.deepEqual(requests, ["/api/tasks/1/dependencies/2"]);

  const dependentRow = page.getByRole("button", { name: "#3 Последователь" }).locator("..");
  await dependentRow.locator("button").last().click();
  await page.getByRole("alert").getByText("Не удалось удалить связь", { exact: false }).waitFor();
  assert.equal(await dependentRow.count(), 1);
  assert.equal(requests[1], "/api/tasks/3/dependencies/1");

  failBlockedRemoval = false;
  await dependentRow.locator("button").last().click();
  await page.getByText("Блокирует (1)").waitFor({ state: "hidden" });
  assert.equal(requests[2], "/api/tasks/3/dependencies/1");
  assert.equal(await page.getByRole("alert").count(), 0);
  console.log("PASS: dependency removal direction and failed deletion retry");
} finally {
  if (browser) {
    await browser.close();
  }
  server.kill();
}
