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
let browser;
let version = 1;
let taskRequests = 0;

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
    localStorage.setItem("akhflow.token", "first-fixture-token");
    localStorage.setItem("akhflow.project", "alpha");
    const originalFetch = window.fetch.bind(window);
    window.streams = [];
    window.fetch = (input, options = {}) => {
      const requestURL = String(input);
      if (!requestURL.endsWith("/events")) return originalFetch(input, options);
      const stream = { url: requestURL, authorization: options.headers.Authorization, signal: options.signal, aborted: false };
      window.streams.push(stream);
      if (window.nextStatus) {
        const status = window.nextStatus;
        window.nextStatus = null;
        return Promise.resolve(new Response(null, { status }));
      }
      const body = new ReadableStream({
        start(controller) {
          stream.controller = controller;
          options.signal.addEventListener("abort", () => {
            stream.aborted = true;
            try {
              controller.error(new DOMException("Aborted", "AbortError"));
            } catch {
              // A stream that was already closed needs no further cleanup.
            }
          });
        },
      });
      return Promise.resolve(new Response(body, { headers: { "Content-Type": "text/event-stream" } }));
    };
    window.sendChunks = (index, chunks) => {
      const encoder = new TextEncoder();
      for (const chunk of chunks) window.streams[index].controller.enqueue(encoder.encode(chunk));
    };
    window.sendSplitBytes = (index, value) => {
      const bytes = new TextEncoder().encode(value);
      const split = bytes.findIndex((byte) => byte > 127) + 1;
      window.streams[index].controller.enqueue(bytes.slice(0, split));
      window.streams[index].controller.enqueue(bytes.slice(split));
    };
  });
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/auth/login") {
      await route.fulfill({ json: { token: "second-fixture-token" } });
      return;
    }
    if (path === "/api/projects") {
      const projects = [{ id: 1, slug: "alpha", name: "Alpha" }, { id: 2, slug: "beta", name: "Beta" }];
      await route.fulfill({ json: projects });
      return;
    }
    const match = path.match(/^\/api\/projects\/(alpha|beta)\/tasks$/);
    if (match) {
      taskRequests++;
      const task = { id: match[1] === "alpha" ? 1 : 2, title: `${match[1]} version ${version}`, status: "todo", priority: "medium" };
      await route.fulfill({ json: [task] });
      return;
    }
    if (path.endsWith("/role")) {
      await route.fulfill({ json: { role: "viewer" } });
      return;
    }
    await route.fulfill({ json: [] });
  });

  await page.goto(url);
  await page.waitForFunction(() => window.streams.some((stream) => !stream.aborted));
  await page.evaluate(() => { window.streams = window.streams.filter((stream) => !stream.aborted); });
  await page.getByText("alpha version 1", { exact: true }).waitFor();
  const first = await page.evaluate(() => ({ url: window.streams[0].url, authorization: window.streams[0].authorization }));
  assert.equal(first.url, "/api/projects/alpha/events");
  assert.equal(first.authorization, "Bearer first-fixture-token");
  assert.ok(!first.url.includes("token"), "token must not appear in URL");
  await delay(150);
  const beforeForeign = taskRequests;
  await page.evaluate(() => window.sendChunks(0, ['data: {"type":"task_created","project_id":2,"title":"foreign"}\n\n']));
  await delay(150);
  assert.equal(taskRequests, beforeForeign, "foreign project event must be ignored");
  assert.equal(await page.getByRole("status").filter({ hasText: "foreign" }).count(), 0);
  version = 2;
  await page.evaluate(() => window.sendChunks(0, [": heartbeat\r", "\ndata: {\"type\":\"task_updated\",\r\n", "data: \"project_id\":1,\"task_id\":1,\"title\":\"Обновлена\"}\r", "\n\r\n"]));
  await page.getByText("alpha version 2", { exact: true }).waitFor();
  await page.getByRole("status").getByText("Изменена задача: Обновлена").waitFor();
  await page.evaluate(() => window.sendSplitBytes(0, 'data: {"type":"task_created","project_id":1,"title":"Привет"}\n\n'));
  await page.getByRole("status").getByText("Создана задача: Привет").waitFor();
  version = 3;
  await page.evaluate(() => window.streams[0].controller.close());
  await page.waitForFunction(() => window.streams.length === 2);
  await page.getByText("alpha version 3", { exact: true }).waitFor();
  await page.locator("header select").first().selectOption("beta");
  await page.waitForFunction(() => window.streams[1].aborted && window.streams.some((stream) => stream.url.includes("/beta/") && !stream.aborted));
  await page.getByText("beta version 3", { exact: true }).waitFor();
  assert.equal(await page.getByText("Создана задача: Привет").count(), 0);
  await page.getByRole("button", { name: "Выйти" }).click();
  await page.waitForFunction(() => window.streams.every((stream) => stream.aborted));
  await page.getByPlaceholder("Email").fill("user@example.invalid");
  await page.getByPlaceholder("Пароль").fill("fixture-password");
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  await page.waitForFunction(() => window.streams.some((stream) => !stream.aborted && stream.authorization === "Bearer second-fixture-token"));
  const next = await page.evaluate(() => {
    const stream = window.streams.find((item) => !item.aborted);
    return { url: stream.url, authorization: stream.authorization };
  });
  assert.equal(next.url, "/api/projects/beta/events");
  assert.equal(next.authorization, "Bearer second-fixture-token");
  const count = await page.evaluate(() => {
    window.nextStatus = 403;
    window.streams.find((stream) => !stream.aborted).controller.close();
    return window.streams.length;
  });
  await page.waitForFunction((previous) => window.streams.length > previous, count);
  await page.waitForTimeout(2200);
  assert.equal(await page.evaluate(() => window.streams.length), count + 1, "access denial must stop retries");
  console.log("PASS: streamed events, project isolation, reconnect, denied access, account switch and bearer credentials");
} finally {
  if (browser) await browser.close();
  server.kill();
}
