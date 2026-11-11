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
const releases = new Map();

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
  page.setDefaultTimeout(5000);
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "account-a");
    localStorage.removeItem("akhflow.project");
  });
  const held = new Set(["Bearer account-a", "Bearer account-b"]);
  const projectRequests = new Map();
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const account = request.headers().authorization;
    if (path === "/api/projects") {
      projectRequests.set(account, (projectRequests.get(account) ?? 0) + 1);
      if (held.has(account)) {
        await new Promise((resolve) => {
          const pending = releases.get(account) ?? [];
          pending.push(resolve);
          releases.set(account, pending);
        });
      }
      const projects = account === "Bearer account-c" ? [] : [
        { id: 1, slug: "alpha", name: "Alpha" },
        { id: 2, slug: "beta", name: "Beta" },
      ];
      await route.fulfill({ json: projects }).catch(() => {});
      return;
    }
    if (path.endsWith("/events")) {
      await route.fulfill({ status: 403, body: "" });
      return;
    }
    const data = path.endsWith("/role") ? { role: "owner" } : [];
    await route.fulfill({ json: data });
  });
  const release = (account) => {
    held.delete(account);
    for (const resolve of releases.get(account) ?? []) {
      resolve();
    }
    releases.delete(account);
  };
  const switchAccount = async (token) => {
    const account = `Bearer ${token}`;
    const previous = projectRequests.get(account) ?? 0;
    await page.evaluate((value) => {
      localStorage.setItem("akhflow.token", value);
      window.dispatchEvent(new StorageEvent("storage", { key: "akhflow.token", storageArea: localStorage }));
    }, token);
    const deadline = Date.now() + 5000;
    while ((projectRequests.get(account) ?? 0) <= previous && Date.now() < deadline) {
      await delay(10);
    }
    assert.ok((projectRequests.get(account) ?? 0) > previous, "new account did not request projects");
  };
  await page.goto(url);
  const input = page.getByPlaceholder("Новая задача...");
  await input.waitFor();
  await page.waitForFunction(() => document.querySelector('input[placeholder="Новая задача..."]')?.disabled);
  assert.equal(await input.isDisabled(), true, "draft input accepts text before projects load");
  release("Bearer account-a");
  await page.waitForFunction(() => !document.querySelector('input[placeholder="Новая задача..."]')?.disabled);
  await input.fill("Alpha draft");
  await page.locator("header select").first().selectOption("beta");
  assert.equal(await input.inputValue(), "");
  await input.fill("Beta draft");
  await page.locator("header select").first().selectOption("alpha");
  assert.equal(await input.inputValue(), "Alpha draft");
  await switchAccount("account-b");
  await page.waitForFunction(() => document.querySelector('input[placeholder="Новая задача..."]')?.disabled);
  assert.equal(await input.inputValue(), "", "draft leaked to another account");
  release("Bearer account-b");
  await page.waitForFunction(() => !document.querySelector('input[placeholder="Новая задача..."]')?.disabled);
  await input.fill("Other account draft");
  await switchAccount("account-a");
  await page.locator("header select").first().selectOption("alpha");
  assert.equal(await input.inputValue(), "Alpha draft", "returning to account lost project draft");
  await switchAccount("account-c");
  await page.waitForFunction(() => document.querySelector('input[placeholder="Новая задача..."]')?.disabled);
  assert.equal(await input.isDisabled(), true, "draft input enabled without projects");
  assert.deepEqual(errors, []);
  console.log("PASS: task input waits for a selected project and drafts stay isolated");
} finally {
  for (const pending of releases.values()) {
    for (const resolve of pending) {
      resolve();
    }
  }
  if (browser) {
    await browser.close();
  }
  server.kill();
}
