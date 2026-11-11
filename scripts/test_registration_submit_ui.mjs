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
    if (new URL(route.request().url()).pathname.endsWith("/events")) {
      await route.fulfill({ status: 403, body: "" });
      return;
    }
    await route.fulfill({ json: [] });
  });
  await context.addInitScript(() => {
    const original = window.fetch.bind(window);
    window.registrationRequests = [];
    window.fetch = (input, options = {}) => {
      if (String(input).endsWith("/api/auth/register")) {
        return new Promise((resolve) => {
          window.registrationRequests.push({
            body: JSON.parse(options.body),
            release: (status, body) => resolve(new Response(JSON.stringify(body), { status })),
          });
        });
      }
      return original(input, options);
    };
  });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  await page.goto(url);
  await page.getByRole("button", { name: "Нет аккаунта? Зарегистрироваться" }).click();
  await page.getByPlaceholder("Имя").fill("First user");
  await page.getByPlaceholder("Email").fill("first@example.invalid");
  await page.getByPlaceholder("Пароль").fill("fixture-password");

  const submit = page.getByRole("button", { name: "Создать аккаунт" });
  await submit.evaluate((button) => {
    button.click();
    button.click();
  });
  await page.waitForFunction(() => window.registrationRequests.length === 1);
  await submit.waitFor({ state: "visible" });
  assert.equal(await submit.isDisabled(), true);
  assert.equal(await page.getByPlaceholder("Имя").isDisabled(), true);
  assert.equal(await page.getByPlaceholder("Email").isDisabled(), true);
  assert.equal(await page.getByPlaceholder("Пароль").isDisabled(), true);
  assert.equal(await page.getByRole("button", { name: "Есть аккаунт? Войти" }).isDisabled(), true);
  assert.equal(await page.evaluate(() => window.registrationRequests.length), 1);
  await page.evaluate(() => {
    window.registrationRequests[0].release(503, { error: "authentication temporarily unavailable" });
  });
  await page.getByText("Сервис временно недоступен. Попробуйте позже.", { exact: true }).waitFor();
  assert.equal(await submit.isEnabled(), true);
  assert.equal(await page.getByPlaceholder("Email").inputValue(), "first@example.invalid");
  await page.getByPlaceholder("Email").fill("second@example.invalid");
  await page.getByRole("button", { name: "Есть аккаунт? Войти" }).click();
  await page.getByRole("button", { name: "Нет аккаунта? Зарегистрироваться" }).click();
  await submit.click();
  await page.waitForFunction(() => window.registrationRequests.length === 2);
  assert.equal(await page.evaluate(() => window.registrationRequests[1].body.email), "second@example.invalid");
  await page.evaluate(() => {
    window.registrationRequests[1].release(201, { token: "fixture-new-account" });
  });
  await page.getByRole("button", { name: "Выйти", exact: true }).waitFor();
  assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), "fixture-new-account");
  assert.equal(await page.evaluate(() => window.registrationRequests.length), 2);
  console.log("PASS: duplicate registration, failure and retry, form controls, successful session");
} finally {
  if (browser) await browser.close();
  server.kill();
}
