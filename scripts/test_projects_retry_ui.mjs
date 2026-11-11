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
  await context.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/events")) {
      await route.fulfill({ status: 403, body: "" });
      return;
    }
    if (path.endsWith("/role")) {
      await route.fulfill({ json: { role: "owner" } });
      return;
    }
    await route.fulfill({ json: [] });
  });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  await page.addInitScript(() => {
    if (!sessionStorage.getItem("test-started")) {
      localStorage.setItem("akhflow.token", "fixture-a");
      sessionStorage.setItem("test-started", "1");
    }
    const original = window.fetch.bind(window);
    window.projectMode = sessionStorage.getItem("project-test-mode") || "fail";
    window.pendingProjects = [];
    window.projectCalls = 0;
    window.fetch = (input, options = {}) => {
      if (String(input).endsWith("/api/projects") && (!options.method || options.method === "GET")) {
        window.projectCalls++;
        const token = options.headers?.Authorization;
        const reply = (status, name) => new Response(
          JSON.stringify(status === 200 ? [{ id: 1, slug: name.toLowerCase(), name }] : { error: "unavailable" }),
          { status, headers: { "Content-Type": "application/json" } },
        );
        if (window.projectMode === "hold") {
          return new Promise((resolve) => window.pendingProjects.push({
            signal: options.signal,
            release: (status, name = "Old") => resolve(reply(status, name)),
          }));
        }
        if (window.projectMode === "fail") return Promise.resolve(reply(503, ""));
        const name = token === "Bearer fixture-b" ? "Beta" : "Alpha";
        return Promise.resolve(reply(200, name));
      }
      return original(input, options);
    };
  });
  await page.goto(url);
  const error = page.getByRole("alert").filter({ hasText: "Не удалось загрузить проекты" });
  const retry = page.getByRole("button", { name: "Повторить" });
  await error.waitFor();
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-a");

  await page.evaluate(() => { window.projectMode = "hold"; });
  await retry.click();
  await page.waitForFunction(() => window.pendingProjects.length === 1);
  await retry.isDisabled().then((disabled) => assert.ok(disabled, "retry must be disabled while loading"));
  await page.evaluate(() => window.pendingProjects.shift().release(503));
  await retry.waitFor({ state: "visible" });
  await page.waitForFunction(() => !document.querySelector('[role="alert"] button')?.disabled);
  await page.evaluate(() => { window.projectMode = "success"; });
  await retry.click();
  await page.locator("header select").first().locator("option", { hasText: "Alpha" }).waitFor({ state: "attached" });
  assert.equal(await error.count(), 0, "successful retry must clear error");
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-a");

  await page.evaluate(() => { window.projectMode = "fail"; });
  await page.reload();
  await error.waitFor();
  await page.evaluate(() => { window.projectMode = "hold"; });
  await retry.click();
  await page.waitForFunction(() => window.pendingProjects.length === 1);
  await page.evaluate(() => {
    window.projectMode = "success";
    localStorage.setItem("akhflow.token", "fixture-b");
    window.dispatchEvent(new StorageEvent("storage", { key: "akhflow.token", storageArea: localStorage }));
  });
  await page.locator("header select").first().locator("option", { hasText: "Beta" }).waitFor({ state: "attached" });
  await page.evaluate(() => window.pendingProjects.shift().release(401));
  await delay(200);
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-b", "old response logged out the new account");
  assert.equal(await error.count(), 0, "old response must not show an error for the new account");
  await page.locator("header select").first().locator("option", { hasText: "Beta" }).waitFor({ state: "attached" });
  await page.evaluate(() => sessionStorage.setItem("project-test-mode", "hold"));
  await page.reload();
  await page.waitForFunction(() => window.pendingProjects.length >= 2);
  await page.evaluate(() => window.pendingProjects.at(-1).release(200, "Beta"));
  await page.locator("header select").first().locator("option", { hasText: "Beta" }).waitFor({ state: "attached" });
  await page.evaluate(() => window.pendingProjects[0].release(503));
  await delay(200);
  assert.equal(await error.count(), 0, "older failed request must not replace the successful result");
  await page.locator("header select").first().locator("option", { hasText: "Beta" }).waitFor({ state: "attached" });
  console.log("PASS: project loading failure, retry and account switch");
} finally {
  if (browser) await browser.close();
  server.kill();
}
