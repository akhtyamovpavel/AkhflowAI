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
  const fresh = async () => {
    const context = await browser.newContext();
    await context.route("**/api/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path.endsWith("/events")) {
        await route.fulfill({ status: 403, body: "" });
        return;
      }
      await route.fulfill({ json: [] });
    });
    await context.addInitScript(() => {
      const original = window.fetch.bind(window);
      window.pendingLogin = [];
      window.fetch = (input, options = {}) => {
        if (String(input).includes("/api/auth/")) {
          return new Promise((resolve) => {
            window.pendingLogin.push((token, status = 200) => {
              resolve(new Response(JSON.stringify({ token, error: "fixture denial" }), { status }));
            });
          });
        }
        return original(input, options);
      };
    });
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    await page.goto(url);
    return { context, page };
  };
  const submit = async (page, account, count) => {
    await page.getByPlaceholder("Email").fill(`${account}@example.invalid`);
    await page.getByPlaceholder("Пароль").fill("fixture-password");
    await page.getByRole("button", { name: "Войти", exact: true }).click();
    await page.waitForFunction((expected) => window.pendingLogin.length === expected, count);
  };
  const release = async (page, index, token, status = 200) => {
    await page.evaluate(({ index, token, status }) => {
      window.pendingLogin[index](token, status);
    }, { index, token, status });
    // Let the resolved fetch, React render and effect cleanups finish before negative assertions.
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  };
  const tokenIs = async (page, token) => {
    assert.equal(await page.evaluate(() => localStorage.getItem("akhflow.token")), token);
  };

  // Both response orders must select the latest submitted account.
  for (const firstResponse of [0, 1]) {
    const { context, page } = await fresh();
    await submit(page, "account-a", 1);
    await submit(page, "account-b", 2);
    await release(page, firstResponse, firstResponse === 0 ? "fixture-a" : "fixture-b");
    await tokenIs(page, firstResponse === 0 ? null : "fixture-b");
    await release(page, 1 - firstResponse, firstResponse === 0 ? "fixture-b" : "fixture-a");
    await tokenIs(page, "fixture-b");
    await page.getByRole("button", { name: "Выйти", exact: true }).click();
    await submit(page, "account-c", 3);
    await release(page, 2, "fixture-c");
    await tokenIs(page, "fixture-c");
    await context.close();
  }

  // A failed earlier attempt must not replace feedback for the current attempt.
  {
    const { context, page } = await fresh();
    await submit(page, "account-a", 1);
    await submit(page, "account-b", 2);
    await release(page, 0, "", 401);
    assert.equal(await page.getByText("401:", { exact: false }).count(), 0);
    await release(page, 1, "", 401);
    await page.getByText("401:", { exact: false }).waitFor();
    await submit(page, "account-c", 3);
    await release(page, 2, "fixture-c");
    await tokenIs(page, "fixture-c");
    await context.close();
  }

  // Real storage events from another tab invalidate pending login, including logout back to null.
  for (const logout of [false, true]) {
    const { context, page } = await fresh();
    await submit(page, "account-a", 1);
    const other = await context.newPage();
    await other.goto(url);
    await submit(other, "account-b", 1);
    await release(other, 0, "fixture-b");
    await page.getByRole("button", { name: "Выйти", exact: true }).waitFor();
    if (logout) {
      await other.getByRole("button", { name: "Выйти", exact: true }).click();
      await page.getByPlaceholder("Email").waitFor();
    }
    await release(page, 0, "fixture-a");
    await tokenIs(page, logout ? null : "fixture-b");
    if (logout) {
      await submit(page, "account-c", 2);
      await release(page, 1, "fixture-c");
      await tokenIs(page, "fixture-c");
    }
    await context.close();
  }
  console.log("PASS: latest login wins, sequential login works, cross-tab session changes invalidate pending login");
} finally {
  if (browser) await browser.close();
  server.kill();
}
