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
const task = { id: 1, title: "Задача", status: "todo", priority: "medium" };
let attachments = [];
let uploads = 0;
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
  const page = await browser.newPage({ acceptDownloads: true });
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "fixture-token");
    localStorage.setItem("akhflow.project", "alpha");
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const requestPath = new URL(request.url()).pathname;
    let data = [];
    if (requestPath === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Альфа" }];
    } else if (requestPath === "/api/projects/alpha/tasks") {
      data = [task];
    } else if (requestPath === "/api/tasks/1") {
      data = { task, dependencies: [], comments: [], blocks: [], history: [] };
    } else if (requestPath === "/api/tasks/1/attachments" && request.method() === "POST") {
      assert.equal(request.headers().authorization, "Bearer fixture-token");
      assert.match(request.headers()["content-type"], /^multipart\/form-data; boundary=/);
      uploads++;
      attachments = [{ id: 7, filename: "file.txt", size_bytes: 7 }];
      data = attachments[0];
    } else if (requestPath === "/api/tasks/1/attachments") {
      assert.equal(request.headers().authorization, "Bearer fixture-token");
      data = attachments;
    } else if (requestPath === "/api/tasks/1/attachments/7") {
      assert.equal(request.headers().authorization, "Bearer fixture-token");
      await route.fulfill({ status: 200, contentType: "application/octet-stream", body: "content" });
      return;
    }
    await route.fulfill({ status: 200, json: data });
  });
  await page.goto(url);
  await page.getByRole("button", { name: "Задача", exact: true }).click();
  await page.getByText("Вложений пока нет.").waitFor();
  const input = page.locator('input[type="file"]');
  await input.setInputFiles({ name: "file.txt", mimeType: "text/plain", buffer: Buffer.from("content") });
  await page.getByRole("button", { name: "Загрузить", exact: true }).click();
  await page.getByRole("button", { name: "file.txt (7 байт)" }).waitFor();
  assert.equal(uploads, 1);
  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: "file.txt (7 байт)" }).click();
  const download = await downloadPromise;
  assert.equal(download.suggestedFilename(), "file.txt");
  await page.locator('input[type="file"]').setInputFiles({ name: "large.bin", mimeType: "application/octet-stream", buffer: Buffer.alloc(10 * 1024 * 1024 + 1) });
  await page.getByRole("button", { name: "Загрузить", exact: true }).click();
  await page.getByRole("alert").getByText("Размер файла не должен превышать 10 МиБ.").waitFor();
  assert.equal(uploads, 1);
  console.log("PASS: attachment upload, download, authentication and size limit");
} finally {
  if (browser) await browser.close();
  server.kill();
}
