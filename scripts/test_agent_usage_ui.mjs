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
  let fail = false;
  await page.route("**/api/**", async (route) => {
    const requestURL = new URL(route.request().url());
    const path = requestURL.pathname;
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Альфа" }, { id: 2, slug: "beta", name: "Бета" }];
    }
    if (path.endsWith("/agent/usage")) {
      if (fail) {
        await route.fulfill({ status: 500, json: { error: "fixture" } });
        return;
      }
      const days = Number(requestURL.searchParams.get("days"));
      const requests = path.includes("/alpha/") ? days : 2;
      data = { days, requests, replies: 1, tool_calls: 3 };
    }
    await route.fulfill({ status: 200, json: data });
  });
  await page.goto(url, { waitUntil: "domcontentloaded" });
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await page.getByText("Запросы", { exact: true }).waitFor();
  assert.equal(await page.locator("dl dd").first().innerText(), "30");
  await page.getByLabel("Период").selectOption("7");
  await page.waitForFunction(() => document.querySelector("dd")?.textContent === "7");
  await page.getByLabel("Период").selectOption("0");
  await page.waitForFunction(() => document.querySelector("dd")?.textContent === "0");
  fail = true;
  await page.getByRole("button", { name: "Обновить", exact: true }).click();
  await page.getByRole("alert").waitFor();
  fail = false;
  await page.getByRole("button", { name: "Обновить", exact: true }).click();
  await page.getByText("Запросы", { exact: true }).waitFor();
  await page.locator("header select").first().selectOption("beta");
  await page.waitForFunction(() => document.querySelector("dd")?.textContent === "2");
  console.log("PASS: agent usage periods, errors, retry and project isolation");
} finally {
  if (browser) {
    await browser.close();
  }
  server.kill("SIGTERM");
}
