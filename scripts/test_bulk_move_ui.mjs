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
  const tasks = [
    { id: 1, title: "Первая", status: "todo", priority: "medium", epic_id: 1 },
    { id: 2, title: "Заблокирована", status: "todo", priority: "medium", epic_id: 1 },
    { id: 3, title: "Уже завершена", status: "done", priority: "medium", epic_id: 2 },
  ];
  const patches = [];
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (request.method() === "PATCH" && path.startsWith("/api/tasks/")) {
      const id = Number(path.split("/").pop());
      patches.push(id);
      if (id === 2) {
        await route.fulfill({ status: 409, json: { error: "unfinished dependency" } });
        return;
      }
      tasks.find((task) => task.id === id).status = request.postDataJSON().status;
      await route.fulfill({ status: 200, json: { ok: true } });
      return;
    }
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Альфа" }, { id: 2, slug: "beta", name: "Бета" }];
    } else if (path === "/api/projects/alpha/tasks") {
      data = tasks;
    } else if (path === "/api/projects/alpha/epics") {
      data = [{ id: 1, title: "Эпик 1" }, { id: 2, title: "Эпик 2" }];
    }
    await route.fulfill({ status: 200, json: data });
  });
  await page.goto(url);
  const bulk = page.getByRole("region", { name: "Массовые операции" });
  await page.getByRole("checkbox", { name: "Выбрать задачу Первая #1" }).waitFor();
  await bulk.getByRole("button", { name: "Выбрать видимые" }).click();
  assert.match(await bulk.innerText(), /Выбрано: 3/);
  await bulk.getByLabel("Статус").selectOption("done");
  await bulk.getByRole("button", { name: "Переместить выбранные" }).click();
  await bulk.getByRole("status").waitFor();
  assert.match(await bulk.innerText(), /Перемещено: 1/);
  assert.match(await bulk.innerText(), /Уже в выбранном статусе: 1/);
  assert.match(await bulk.innerText(), /Не удалось переместить: #2/);
  assert.deepEqual(patches, [1, 2]);
  assert.equal(await page.getByRole("checkbox", { name: "Выбрать задачу Заблокирована #2" }).isChecked(), true);
  assert.equal(await page.getByRole("checkbox", { name: "Выбрать задачу Первая #1" }).isChecked(), false);
  await page.getByRole("button", { name: "Список", exact: true }).click();
  assert.equal(await page.getByRole("checkbox", { name: "Выбрать задачу Заблокирована #2" }).isChecked(), true);
  await page.locator("header select").nth(1).selectOption("2");
  assert.match(await bulk.innerText(), /Выбрано: 0/);
  await bulk.getByRole("button", { name: "Выбрать видимые" }).click();
  assert.match(await bulk.innerText(), /Выбрано: 1/);
  await page.locator("header select").first().selectOption("beta");
  assert.match(await bulk.innerText(), /Выбрано: 0/);
  console.log("PASS: bulk move, partial failure, unchanged task, views and selection clearing");
} finally {
  if (browser) {
    await browser.close();
  }
  server.kill();
}
