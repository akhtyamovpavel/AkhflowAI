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
const base = `http://127.0.0.1:${port}`;
const server = spawn("./node_modules/.bin/vite", ["--host", "127.0.0.1", "--port", String(port), "--strictPort"], {
  cwd: new URL("../web", import.meta.url),
  stdio: "ignore",
});

let browser;
try {
  let ready = false;
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      ready = (await fetch(base)).ok;
    } catch {
      await delay(100);
    }
    if (ready) break;
  }
  assert.ok(ready, "Vite did not start");
  browser = await playwright.chromium.launch({ executablePath: "/usr/bin/google-chrome", headless: true });
  const task = { id: 1, title: "Обсудить & проверить #1?", status: "backlog", priority: "low" };
  const configure = async (context, outsider = false) => {
    await context.route("**/api/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      let data = [];
      if (path === "/api/auth/login") data = { token: "test-session" };
      if (path === "/api/projects") {
        data = outsider ? [{ id: 2, slug: "beta", name: "Бета" }] : [
          { id: 1, slug: "alpha", name: "Альфа" },
          { id: 2, slug: "beta", name: "Бета" },
        ];
      }
      if (path === "/api/projects/alpha/tasks") data = [task];
      if (path === "/api/projects/alpha/role" || path === "/api/projects/beta/role") data = { role: "viewer" };
      if (path === "/api/tasks/1") {
        assert.equal(outsider, false, "outsider must not request task details");
        data = { task, comments: [], dependencies: [], blocks: [], history: [] };
      }
      await route.fulfill({ json: data });
    });
  };

  const sender = await browser.newContext();
  await configure(sender);
  await sender.addInitScript(() => {
    localStorage.setItem("akhflow.token", "private-session-token");
    window.open = (url, target, features) => {
      window.capturedShare = { url, target, features };
      return null;
    };
  });
  const page = await sender.newPage();
  await page.goto(`${base}/app/?credential=private-query#private-fragment`);
  await page.getByRole("button", { name: task.title, exact: true }).click();
  await page.getByRole("button", { name: "Обсудить в Telegram" }).click();
  const captured = await page.evaluate(() => window.capturedShare);
  assert.equal(captured.target, "_blank");
  assert.match(captured.features, /noopener/);
  assert.match(captured.features, /noreferrer/);
  const telegram = new URL(captured.url);
  assert.equal(telegram.origin, "https://t.me");
  assert.equal(telegram.pathname, "/share/url");
  assert.equal(telegram.searchParams.get("text"), task.title);
  const link = new URL(telegram.searchParams.get("url"));
  assert.equal(link.pathname, "/app/");
  assert.equal(link.searchParams.get("task_project"), "alpha");
  assert.equal(link.searchParams.get("task"), "1");
  assert.equal(link.searchParams.size, 2);
  assert.equal(link.hash, "");
  assert.doesNotMatch(captured.url, /private-session-token|private-query|private-fragment/);
  const credentialShare = await page.evaluate(async () => {
    const { telegramShareURL } = await import("/src/lib/taskShare.ts");
    const location = { href: "https://fixture-user:fixture-password@example.invalid/app/?private=query#fragment" };
    return telegramShareURL(location, "alpha", 1, "title");
  });
  const credentialLink = new URL(new URL(credentialShare).searchParams.get("url"));
  assert.equal(credentialLink.username, "");
  assert.equal(credentialLink.password, "");
  assert.equal(credentialLink.pathname, "/app/");
  assert.doesNotMatch(credentialShare, /fixture-user|fixture-password|private|fragment/);

  const receiver = await browser.newContext();
  await configure(receiver);
  await receiver.addInitScript(() => localStorage.setItem("akhflow.project", "beta"));
  const receiverPage = await receiver.newPage();
  await receiverPage.goto(link.href);
  await receiverPage.getByPlaceholder("Email").fill("viewer@example.invalid");
  await receiverPage.getByPlaceholder("Пароль").fill("fixture-password");
  await receiverPage.getByRole("button", { name: "Войти", exact: true }).click();
  await receiverPage.getByRole("button", { name: "Закрыть карточку задачи" }).waitFor();
  assert.ok(await receiverPage.locator("input").evaluateAll((items, title) => items.some((item) => item.value === title), task.title));
  assert.equal(await receiverPage.locator("header select").first().inputValue(), "alpha");
  await receiverPage.getByRole("button", { name: "Закрыть карточку задачи" }).click();
  await receiverPage.waitForTimeout(100);
  assert.equal(await receiverPage.getByRole("button", { name: "Закрыть карточку задачи" }).count(), 0);

  const denied = await browser.newContext();
  await configure(denied, true);
  await denied.addInitScript(() => localStorage.setItem("akhflow.token", "outsider-session"));
  const deniedPage = await denied.newPage();
  await deniedPage.goto(link.href);
  await deniedPage.locator("header select").first().waitFor();
  assert.equal(await deniedPage.locator("header select").first().inputValue(), "beta");
  assert.equal(await deniedPage.getByRole("button", { name: "Обсудить в Telegram" }).count(), 0);

  const mismatched = await receiver.newPage();
  await mismatched.goto(`${base}/app/?task_project=beta&task=1`);
  await mismatched.locator("header select").first().waitFor();
  await mismatched.waitForTimeout(200);
  assert.equal(await mismatched.getByRole("button", { name: "Обсудить в Telegram" }).count(), 0);
  console.log("PASS: Telegram share, safe URL, login, project access and deployment path");
} finally {
  if (browser) await browser.close();
  server.kill();
}
