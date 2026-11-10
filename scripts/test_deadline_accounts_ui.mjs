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
const token = (id, version) => `fixture.${Buffer.from(JSON.stringify({ sub: String(id), version })).toString("base64url")}.signature`;
const first = token(1, 1);
const rotated = token(1, 2);
const second = token(2, 1);
let browser;

try {
  let ready = false;
  for (let attempt = 0; attempt < 100; attempt++) {
    try { ready = (await fetch(url)).ok; } catch { await delay(100); }
    if (ready) break;
  }
  assert.ok(ready, "Vite did not start");
  browser = await playwright.chromium.launch({ executablePath: "/usr/bin/google-chrome", headless: true });
  const page = await browser.newPage();
  page.setDefaultTimeout(5000);
  await page.addInitScript(() => {
    window.delivered = [];
    window.Notification = class {
      static permission = "granted";
      constructor(title, options) { window.delivered.push({ title, ...options }); }
      close() {}
    };
  });
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const due = new Date();
    const day = `${due.getFullYear()}-${String(due.getMonth() + 1).padStart(2, "0")}-${String(due.getDate()).padStart(2, "0")}`;
    let data = [];
    if (path === "/api/projects") data = [{ id: 1, slug: "alpha", name: "Alpha" }, { id: 2, slug: "beta", name: "Beta" }];
    if (/^\/api\/projects\/(alpha|beta)\/tasks$/.test(path)) data = [{ id: 1, title: "Task", status: "todo", priority: "medium", due_date: day }];
    if (path.endsWith("/role")) data = { role: "viewer" };
    await route.fulfill({ status: 200, json: data });
  });
  await page.goto(url);
  await page.evaluate(({ first }) => {
    localStorage.setItem("akhflow.token", first);
    localStorage.setItem("akhflow.project", "alpha");
  }, { first });
  await page.reload();
  await page.waitForFunction(() => window.delivered.length === 1);
  await page.reload();
  await page.getByText("Task", { exact: false }).first().waitFor();
  await page.waitForTimeout(200);
  assert.equal(await page.evaluate(() => window.delivered.length), 0, "same user must be deduplicated after reload");
  await page.evaluate((rotated) => localStorage.setItem("akhflow.token", rotated), rotated);
  await page.reload();
  await page.getByText("Task", { exact: false }).first().waitFor();
  await page.waitForTimeout(200);
  assert.equal(await page.evaluate(() => window.delivered.length), 0, "refreshed token must keep the same user identity");
  await page.evaluate((second) => localStorage.setItem("akhflow.token", second), second);
  await page.reload();
  await page.waitForFunction(() => window.delivered.length === 1);
  await page.locator("header select").first().selectOption("beta");
  await page.waitForFunction(() => window.delivered.length === 2);
  const keys = await page.evaluate(() => Object.keys(localStorage).filter((key) => key.startsWith("akhflow.deadline.")));
  assert.ok(keys.every((key) => [first, rotated, second].every((value) => !key.includes(value))), "reminder keys must not include bearer tokens");
  console.log("PASS: user and project isolation, token rotation and private reminder keys");
} finally {
  if (browser) await browser.close();
  server.kill();
}
