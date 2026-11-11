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
  for (let attempt = 0; attempt < 150; attempt++) {
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
  page.setDefaultTimeout(10000);
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "fixture-a");
    localStorage.setItem("akhflow.project", "alpha");
    const originalFetch = window.fetch.bind(window);
    window.streams = [];
    window.epicMode = "initial";
    window.epicRequests = [];
    window.fetch = (input, options = {}) => {
      const path = String(input);
      if (path.endsWith("/events")) {
        const stream = { signal: options.signal };
        window.streams.push(stream);
        const body = new ReadableStream({
          start(controller) {
            stream.controller = controller;
            options.signal.addEventListener("abort", () => {
              try {
                controller.error(new DOMException("Aborted", "AbortError"));
              } catch {
                // A closed stream needs no further cleanup.
              }
            });
          },
        });
        return Promise.resolve(new Response(body, { headers: { "Content-Type": "text/event-stream" } }));
      }
      if (!path.endsWith("/epics")) {
        return originalFetch(input, options);
      }
      const reply = (title) => {
        const payload = [{ id: 1, title, status: "open", description: "" }];
        return new Response(JSON.stringify(payload), { headers: { "Content-Type": "application/json" } });
      };
      const request = { mode: window.epicMode, signal: options.signal, delivered: false };
      window.epicRequests.push(request);
      if (request.mode === "hold") {
        return new Promise((resolve) => {
          request.release = () => {
            resolve(reply("Old epic"));
            request.delivered = true;
          };
        });
      }
      const title = request.mode === "new" ? "Created epic" : "Alpha epic";
      return Promise.resolve(reply(title));
    };
    window.importEvent = () => {
      const stream = window.streams.findLast((item) => !item.signal.aborted);
      const event = JSON.stringify({ type: "tasks_imported", project_id: 1 });
      stream.controller.enqueue(new TextEncoder().encode(`data: ${event}\n\n`));
    };
  });
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/projects") {
      const projects = [{ id: 1, slug: "alpha", name: "Alpha" }];
      await route.fulfill({ json: projects });
      return;
    }
    if (path.endsWith("/role")) {
      await route.fulfill({ json: { role: "owner" } });
      return;
    }
    await route.fulfill({ json: [] });
  });
  await page.goto(url, { timeout: 30000 });
  const epicSelect = page.locator("header select").nth(1);
  await epicSelect.locator("option", { hasText: "Alpha epic" }).waitFor({ state: "attached" });
  await page.waitForFunction(() => window.streams.some((stream) => !stream.signal.aborted));
  await page.evaluate(() => {
    window.epicMode = "hold";
    window.importEvent();
  });
  await page.waitForFunction(() => window.epicRequests.some((request) => request.mode === "hold" && request.release));
  await page.evaluate(() => {
    window.epicMode = "new";
    window.importEvent();
  });
  await epicSelect.locator("option", { hasText: "Created epic" }).waitFor({ state: "attached" });
  await page.evaluate(() => window.epicRequests.find((request) => request.mode === "hold").release());
  await page.waitForFunction(() => window.epicRequests.some((request) => request.mode === "hold" && request.delivered));
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  assert.equal(await epicSelect.locator("option", { hasText: "Created epic" }).count(), 1, "newer epic list must remain visible");
  assert.equal(await epicSelect.locator("option", { hasText: "Old epic" }).count(), 0, "late stale response must not replace newer epics");
  console.log("PASS: stale SSE epic response cannot overwrite a newer list");
} finally {
  if (browser) {
    await browser.close();
  }
  server.kill();
}
