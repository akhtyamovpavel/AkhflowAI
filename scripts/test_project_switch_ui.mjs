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
const held = [];
let completedHeld = 0;
let releaseHeld;
const heldUntilReleased = new Promise((resolve) => {
  releaseHeld = resolve;
});
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
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "fixture-token");
    localStorage.setItem("akhflow.project", "alpha");
  });
  let phase = "initial";
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/projects") {
      const projects = [
        { id: 1, slug: "alpha", name: "Альфа" },
        { id: 2, slug: "beta", name: "Бета" },
      ];
      await route.fulfill({ json: projects });
      return;
    }
    if (path === "/api/tasks/1") {
      const task = { id: 1, title: "Задача alpha-alpha-current", status: "backlog", priority: "low", assignee_id: 1 };
      await route.fulfill({ json: { task, comments: [], dependencies: [], blocks: [], history: [] } });
      return;
    }
    const match = path.match(/^\/api\/projects\/(alpha|beta)\/(tasks|epics|members|chats)$/);
    if (!match) {
      await route.fulfill({ json: [] });
      return;
    }
    const [, project, resource] = match;
    const currentPhase = phase;
    if (currentPhase.endsWith("-held")) {
      held.push({ project, resource });
      await heldUntilReleased;
    }
    const name = `${project}-${currentPhase}`;
    let data = [];
    if (resource === "tasks") {
      data = [{ id: project === "alpha" ? 1 : 2, title: `Задача ${name}`, status: "backlog", priority: "low" }];
    }
    if (resource === "epics") {
      data = [{ id: project === "alpha" ? 1 : 2, title: `Эпик ${name}` }];
    }
    if (resource === "members") {
      data = [{ id: project === "alpha" ? 1 : 2, display_name: `Участник ${name}` }];
    }
    if (resource === "chats") {
      data = [{ id: project === "alpha" ? 1 : 2, title: `Чат ${name}` }];
    }
    await route.fulfill({ json: data });
    if (currentPhase.endsWith("-held")) {
      completedHeld++;
    }
  });

  const waitForHeld = async (project) => {
    const resources = ["tasks", "epics", "members", "chats"];
    for (let attempt = 0; attempt < 100; attempt++) {
      if (resources.every((resource) => held.some((item) => item.project === project && item.resource === resource))) {
        return;
      }
      await delay(50);
    }
    throw new Error(`Requests were not held for ${project}`);
  };

  await page.goto(url);
  await page.getByText("Задача alpha-initial", { exact: true }).waitFor();
  const projectSelect = page.locator("header select").first();
  phase = "beta-held";
  await projectSelect.selectOption("beta");
  await waitForHeld("beta");
  assert.equal(await page.getByText("Задача alpha-initial", { exact: true }).count(), 0);
  assert.equal(await page.getByText("Эпик alpha-initial", { exact: true }).count(), 0);

  phase = "alpha-held";
  await page.locator("header select").first().selectOption("alpha");
  await waitForHeld("alpha");
  phase = "beta-current";
  await page.locator("header select").first().selectOption("beta");
  await page.getByText("Задача beta-beta-current", { exact: true }).waitFor();
  phase = "alpha-current";
  await page.locator("header select").first().selectOption("alpha");
  await page.getByText("Задача alpha-alpha-current", { exact: true }).waitFor();
  releaseHeld();
  for (let attempt = 0; attempt < 100 && completedHeld < held.length; attempt++) {
    await delay(50);
  }
  assert.equal(completedHeld, held.length, "Held responses must be delivered before assertions");
  await delay(100);
  assert.equal(await page.getByText("Задача alpha-alpha-current", { exact: true }).count(), 1);
  assert.equal(await page.getByText("Эпик alpha-alpha-current", { exact: true }).count(), 1);
  assert.doesNotMatch(await page.locator("body").innerText(), /beta-held|alpha-held|beta-current/);
  await page.getByText("Задача alpha-alpha-current", { exact: true }).click();
  await page.getByText("Участник alpha-alpha-current", { exact: true }).waitFor({ state: "attached" });
  assert.equal(await page.getByText("Участник alpha-alpha-held", { exact: true }).count(), 0);
  await page.locator(".fixed.inset-0.z-50").click({ position: { x: 5, y: 5 } });
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await page.getByText("Чат alpha-alpha-current", { exact: true }).waitFor();
  assert.doesNotMatch(await page.locator("body").innerText(), /beta-held|alpha-held|beta-current/);
  console.log("PASS: project switches discard stale tasks, epics, members and chats");
} finally {
  releaseHeld();
  if (browser) {
    await browser.close();
  }
  server.kill();
}
