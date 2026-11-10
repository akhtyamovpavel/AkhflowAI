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
  const page = await browser.newPage();
  page.setDefaultTimeout(5000);
  await page.addInitScript(() => {
    const original = window.fetch.bind(window);
    window.pending403 = [];
    window.fetch = (input, options = {}) => {
      const path = String(input);
      if (path.endsWith("/tasks") && options.method === "POST") {
        const forbidden = () => new Response('{"error":"forbidden"}', { status: 403 });
        if (window.failureMode === "hold") {
          return new Promise((resolve) => window.pending403.push(() => resolve(forbidden())));
        }
        if (window.failureMode === "immediate") return Promise.resolve(forbidden());
      }
      return original(input, options);
    };
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/auth/login") {
      const account = request.postDataJSON().email.split("@")[0];
      await route.fulfill({ json: { token: `fixture-${account}` } });
      return;
    }
    if (path.endsWith("/events")) {
      await route.fulfill({ status: 403, body: "" });
      return;
    }
    let data = [];
    if (path === "/api/projects") data = [{ id: 1, slug: "alpha", name: "Alpha" }, { id: 2, slug: "beta", name: "Beta" }];
    if (path.endsWith("/role")) data = { role: "owner" };
    await route.fulfill({ json: data });
  });
  const login = async (account) => {
    await page.getByPlaceholder("Email").fill(`${account}@example.invalid`);
    await page.getByPlaceholder("Пароль").fill("fixture-password");
    await page.getByRole("button", { name: "Войти", exact: true }).click();
    await page.locator("header select").first().waitFor();
  };
  const submit = async (title) => {
    const input = page.getByPlaceholder("Новая задача...");
    await input.fill(title);
    await input.press("Enter");
  };
  const alert = page.getByText("Недостаточно прав для выполнения действия.", { exact: false });
  await page.goto(url);
  await login("account-a");
  await page.waitForFunction(() => document.querySelector("header select")?.value === "alpha");
  await page.evaluate(() => { window.failureMode = "hold"; });
  if (!process.argv.includes("--account-only")) {
    await submit("old project");
    await page.waitForFunction(() => window.pending403.length === 1);
    await page.locator("header select").first().selectOption("beta");
    await page.evaluate(() => window.pending403.shift()());
    await delay(400);
    assert.equal(await alert.count(), 0, "old project request displayed an error in the new project");

    await submit("old project after return");
    await page.waitForFunction(() => window.pending403.length === 1);
    await page.locator("header select").first().selectOption("alpha");
    await page.locator("header select").first().selectOption("beta");
    await page.evaluate(() => window.pending403.shift()());
    await delay(400);
    assert.equal(await alert.count(), 0, "request from an earlier project visit displayed an error");
  }

  await submit("old account");
  await page.waitForFunction(() => window.pending403.length === 1);
  await page.getByRole("button", { name: "Выйти" }).click();
  await login("account-b");
  await page.evaluate(() => window.pending403.shift()());
  await delay(400);
  assert.equal(await alert.count(), 0, "old account request displayed an error in the new account");
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-account-b");

  await page.evaluate(async () => {
    const { api } = await import("/src/App.tsx");
    window.abortController = new AbortController();
    window.cancelledCallbacks = 0;
    const options = { method: "POST", signal: window.abortController.signal };
    const onForbidden = () => { window.cancelledCallbacks++; };
    window.cancelledRequest = api("/api/projects/beta/tasks", options, "fixture-account-b", onForbidden).catch(() => {});
  });
  await page.waitForFunction(() => window.pending403.length === 1);
  await page.evaluate(async () => {
    window.abortController.abort();
    window.pending403.shift()();
    await window.cancelledRequest;
  });
  assert.equal(await page.evaluate(() => window.cancelledCallbacks), 0, "cancelled request displayed an error");
  assert.equal(await alert.count(), 0, "cancelled request displayed a workspace error");

  await page.evaluate(() => { window.failureMode = "immediate"; });
  await submit("current project");
  await alert.waitFor();
  console.log("PASS: stale and cancelled 403 ignored, current denial displayed");
} finally {
  if (browser) await browser.close();
  server.kill();
}
