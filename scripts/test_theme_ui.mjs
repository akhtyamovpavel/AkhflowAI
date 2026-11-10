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
const serverArgs = ["--host", "127.0.0.1", "--port", String(port), "--strictPort"];
const serverOptions = { cwd: new URL("../web", import.meta.url), stdio: "ignore" };
const server = spawn("./node_modules/.bin/vite", serverArgs, serverOptions);

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
  const context = await browser.newContext({ colorScheme: "light" });
  await context.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Альфа" }];
    }
    await route.fulfill({ status: 200, json: data });
  });
  const page = await context.newPage();
  await page.goto(url);
  const toggle = page.getByRole("button", { name: "Тёмная тема" });
  await toggle.waitFor();
  assert.equal(await toggle.getAttribute("aria-pressed"), "false");
  const lightBackground = await page.locator("body").evaluate((el) => getComputedStyle(el).backgroundColor);
  await toggle.focus();
  await page.keyboard.press("Space");
  assert.equal(await toggle.getAttribute("aria-pressed"), "true");
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.theme")), "dark");
  const darkBackground = await page.locator("body").evaluate((el) => getComputedStyle(el).backgroundColor);
  assert.notEqual(darkBackground, lightBackground);
  assert.equal(await page.locator("html").evaluate((el) => getComputedStyle(el).colorScheme), "dark");
  await page.reload();
  await toggle.waitFor();
  assert.equal(await toggle.getAttribute("aria-pressed"), "true");
  await page.evaluate(() => localStorage.setItem("akhflow.token", "fixture-token"));
  await page.reload();
  await page.getByRole("button", { name: "Выйти" }).waitFor();
  assert.equal(await toggle.getAttribute("aria-pressed"), "true");
  const secondPage = await context.newPage();
  await secondPage.goto(url);
  const secondToggle = secondPage.getByRole("button", { name: "Тёмная тема" });
  await secondToggle.waitFor();
  await secondToggle.click();
  await page.waitForFunction(() => !document.documentElement.classList.contains("dark"));
  assert.equal(await toggle.getAttribute("aria-pressed"), "false");
  await page.reload();
  await toggle.waitFor();
  assert.equal(await toggle.getAttribute("aria-pressed"), "false");
  const systemContext = await browser.newContext({ colorScheme: "dark" });
  const systemPage = await systemContext.newPage();
  await systemPage.goto(url);
  const systemToggle = systemPage.getByRole("button", { name: "Тёмная тема" });
  await systemToggle.waitFor();
  assert.equal(await systemToggle.getAttribute("aria-pressed"), "true");
  console.log("PASS: theme toggle, keyboard, persistence, login, app, cross-tab and system preference");
} finally {
  if (browser) {
    await browser.close();
  }
  server.kill();
}
