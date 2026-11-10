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

const makeTask = (id, title, status, assignee_id) => ({ id, title, status, assignee_id, priority: "medium" });
const tasks = {
  alpha: [makeTask(1, "Задача Альфы", "backlog", null), makeTask(2, "А", "todo", 2), makeTask(3, "Б", "in_progress", 3), makeTask(4, "Готово", "done", 2), makeTask(5, "Не участник", "todo", 99)],
  beta: [makeTask(6, "Задача Беты", "todo", null), makeTask(7, "Занят", "todo", 4)],
  empty: [makeTask(8, "Пустой проект", "backlog", null)],
  viewers: [makeTask(9, "Проект читателей", "backlog", null)],
};
const members = {
  alpha: [{ id: 3, display_name: "Борис", email: "b@example.test", role: "editor" }, { id: 2, display_name: "Анна", email: "a@example.test", role: "member" }, { id: 1, display_name: "Читатель", email: "reader@example.test", role: "viewer" }],
  beta: [{ id: 4, display_name: "Вера", email: "v@example.test" }, { id: 5, display_name: "Глеб", email: "g@example.test" }],
  empty: [],
  viewers: [{ id: 6, display_name: "Только читатель", email: "reader2@example.test", role: "viewer" }],
};
let browser;
let mutations = 0;
try {
  let ready = false;
  for (let attempt = 0; attempt < 100; attempt++) {
    try { ready = (await fetch(url)).ok; } catch { await delay(100); }
    if (ready) break;
  }
  assert.ok(ready, "Vite did not start");
  browser = await playwright.chromium.launch({ executablePath: "/usr/bin/google-chrome", headless: true });
  const page = await browser.newPage();
  await page.addInitScript(() => {
    localStorage.setItem("akhflow.token", "fixture-token");
    localStorage.setItem("akhflow.project", "alpha");
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (request.method() !== "GET") mutations++;
    let data = [];
    if (path === "/api/projects") {
      data = Object.keys(tasks).map((slug, index) => ({ id: index + 1, slug, name: slug }));
    } else {
      const projectData = path.match(/^\/api\/projects\/(alpha|beta|empty|viewers)\/(tasks|members)$/);
      const taskDetail = path.match(/^\/api\/tasks\/(\d+)$/);
      if (projectData) data = projectData[2] === "tasks" ? tasks[projectData[1]] : members[projectData[1]];
      if (taskDetail) data = { task: Object.values(tasks).flat().find((task) => task.id === Number(taskDetail[1])), comments: [], dependencies: [], blocks: [], history: [] };
    }
    await route.fulfill({ status: 200, json: data });
  });
  await page.goto(url);
  await page.getByRole("button", { name: "Задача Альфы", exact: true }).click();
  const suggestion = page.getByLabel("Рекомендация исполнителя");
  await suggestion.getByText("Рекомендуем: Анна · Незавершённых задач: 1").waitFor();
  await suggestion.getByText(/При равенстве выбран участник с меньшим ID/).waitFor();

  await page.locator("header select").first().selectOption("beta");
  await page.getByRole("button", { name: "Задача Беты", exact: true }).click();
  await suggestion.getByText("Рекомендуем: Глеб · Незавершённых задач: 0").waitFor();
  assert.equal(await suggestion.getByText(/Анна|Борис/).count(), 0);

  await page.locator("header select").first().selectOption("empty");
  await page.getByRole("button", { name: "Пустой проект", exact: true }).click();
  await page.getByText("Нет участников с правом редактирования для рекомендации.").waitFor();
  await page.locator("header select").first().selectOption("viewers");
  await page.getByRole("button", { name: "Проект читателей", exact: true }).click();
  await page.getByText("Нет участников с правом редактирования для рекомендации.").waitFor();
  assert.equal(mutations, 0, "suggestions must not change an assignee");
  console.log("PASS: workload, tie-breaking, project isolation, empty members and read-only behavior");
} finally {
  if (browser) await browser.close();
  server.kill();
}
