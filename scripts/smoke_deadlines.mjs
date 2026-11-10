import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { readFile, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import ts from "../web/node_modules/typescript/lib/typescript.js";

const source = await readFile(new URL("../web/src/lib/deadlines.ts", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext } });
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled.outputText).toString("base64")}`;
const { upcomingDeadlines, reminderKey, reminderUserId } = await import(moduleURL);
const now = new Date(2026, 9, 6, 23, 59);
const fixture = [
  { id: 1, title: "Today", status: "todo", due_date: "2026-10-06T00:00:00Z" },
  { id: 2, title: "Tomorrow", status: "in_progress", due_date: "2026-10-07" },
  { id: 3, title: "Done", status: "done", due_date: "2026-10-07" },
  { id: 4, title: "Overdue", status: "todo", due_date: "2026-10-05" },
  { id: 5, title: "Future", status: "todo", due_date: "2026-10-08" },
  { id: 6, title: "No deadline", status: "todo" },
  { id: 7, title: "Master", status: "todo", due_date: "2026-10-07", recurring: "daily" },
];
assert.deepEqual(upcomingDeadlines(fixture, now).map((task) => task.id), [1, 2]);
const yearEnd = new Date(2026, 11, 31, 23, 59);
const yearTask = { id: 8, title: "New year", status: "todo", due_date: "2027-01-01" };
assert.equal(upcomingDeadlines([yearTask], yearEnd).length, 1);
const dstTask = { ...yearTask, due_date: "2026-03-09" };
assert.equal(upcomingDeadlines([dstTask], new Date(2026, 2, 8, 23, 59)).length, 1);
assert.notEqual(reminderKey("1", "a", fixture[0]), reminderKey("1", "b", fixture[0]));
assert.notEqual(reminderKey("1", "a", fixture[0]), reminderKey("2", "a", fixture[0]));
assert.notEqual(reminderKey("1", "a", fixture[0]), reminderKey("1", "a", { ...fixture[0], due_date: "2026-10-07" }));
const token = (subject) => `header.${Buffer.from(JSON.stringify({ sub: subject })).toString("base64url")}.signature`;
assert.equal(reminderUserId(token("123")), "123");
assert.equal(reminderUserId(token("0")), null);
assert.equal(reminderUserId("invalid"), null);
console.log("PASS: calendar boundaries, completed/overdue/recurring exclusions, reminder keys");

if (process.argv.includes("--unit")) {
  process.exit(0);
}

const apiURL = process.env.AKHFLOW_API ?? "http://localhost:8080";
const webURL = process.env.AKHFLOW_WEB ?? "http://localhost:5173";
let authToken;
const taskIDs = [];
async function request(path, method = "GET", body) {
  const response = await fetch(apiURL + path, {
    method,
    headers: { "Content-Type": "application/json", ...(authToken ? { Authorization: `Bearer ${authToken}` } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  assert.ok(response.ok, `${method} ${path}: ${response.status}`);
  return response.json();
}

const profile = await mkdtemp(join(tmpdir(), "akhflow-deadlines-"));
const chrome = spawn(process.env.CHROME_BIN ?? "/usr/bin/google-chrome", [
  "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run",
  "--remote-debugging-port=0", `--user-data-dir=${profile}`, "about:blank",
], { stdio: "ignore" });
let socket;
let seq = 0;
const pending = new Map();
async function until(check) {
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      const result = await check();
      if (result) {
        return result;
      }
    } catch {
      // Startup and page rendering can take a few attempts.
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error("Timed out waiting for browser assertion");
}
function cdp(method, params = {}) {
  const id = ++seq;
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      pending.delete(id);
      reject(new Error(`CDP timeout: ${method}`));
    }, 10_000);
    pending.set(id, { resolve, reject, timeout });
    socket.send(JSON.stringify({ id, method, params }));
  });
}
async function evaluate(expression) {
  const result = await cdp("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
  assert.ok(!result.exceptionDetails, JSON.stringify(result.exceptionDetails));
  return result.result.value;
}

try {
  assert.equal((await request("/api/health")).status, "ok");
  const suffix = `${Date.now()}-${process.pid}`;
  authToken = (await request("/api/auth/register", "POST", {
    email: `smoke-deadlines-${suffix}@example.com`, password: `Smoke-${suffix}!`, display_name: "Smoke deadlines",
  })).token;
  const slug = `smoke-deadlines-${suffix}`;
  await request("/api/projects", "POST", { slug, name: "Smoke deadlines" });
  const today = new Date().toLocaleDateString("sv-SE", { timeZone: "Europe/Moscow" });
  const tomorrow = new Date(`${today}T12:00:00Z`);
  tomorrow.setUTCDate(tomorrow.getUTCDate() + 1);
  const nextDate = tomorrow.toISOString().slice(0, 10);
  for (const [title, status, due] of [
    ["Today reminder", "todo", today], ["Tomorrow reminder", "todo", nextDate],
    ["Completed excluded", "done", today], ["No deadline excluded", "todo", null],
  ]) {
    const task = await request(`/api/projects/${slug}/tasks`, "POST", {
      title, status, due_date: due ? `${due}T00:00:00Z` : null,
    });
    taskIDs.push(task.id);
  }
  const port = await until(async () => (await readFile(join(profile, "DevToolsActivePort"), "utf8")).split("\n")[0]);
  const targets = await (await fetch(`http://localhost:${port}/json/list`)).json();
  socket = new WebSocket(targets.find((target) => target.type === "page").webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    socket.addEventListener("open", resolve, { once: true });
    socket.addEventListener("error", reject, { once: true });
  });
  socket.addEventListener("message", (event) => {
    const message = JSON.parse(event.data);
    const entry = pending.get(message.id);
    if (!entry) {
      return;
    }
    pending.delete(message.id);
    clearTimeout(entry.timeout);
    if (message.error) {
      entry.reject(new Error(JSON.stringify(message.error)));
    } else {
      entry.resolve(message.result);
    }
  });
  await cdp("Page.enable");
  await cdp("Emulation.setTimezoneOverride", { timezoneId: "Europe/Moscow" });
  await cdp("Browser.grantPermissions", { origin: webURL, permissions: ["notifications"] });
  const setup = `
    localStorage.setItem('akhflow.token', ${JSON.stringify(authToken)});
    localStorage.setItem('akhflow.project', ${JSON.stringify(slug)});
    window.delivered = [];
    const NativeNotification = window.Notification;
    window.Notification = class extends NativeNotification {
      constructor(title, options) {
        super(title, options);
        window.delivered.push(this);
      }
    };
  `;
  await cdp("Page.addScriptToEvaluateOnNewDocument", { source: setup });
  await cdp("Page.navigate", { url: webURL });
  const panel = "document.querySelector('[aria-label=\"Напоминания о дедлайнах\"]')";
  await until(() => evaluate(`${panel}?.innerText.includes('Tomorrow reminder') && window.delivered.length === 2`));
  assert.equal(await evaluate(`${panel}.querySelectorAll('li').length`), 2);
  assert.equal(await evaluate(`${panel}.innerText.includes('Completed excluded')`), false);
  await evaluate("window.delivered[0].onclick()");
  await until(() => evaluate("!!document.querySelector('input[value=\"Today reminder\"]')"));
  console.log("PASS: REST fixture, UI reminders, native browser notifications, click opens task");
  await cdp("Page.reload");
  await until(() => evaluate(`${panel}?.querySelectorAll('li').length === 2`));
  assert.equal(await evaluate("window.delivered.length"), 0);
  await request(`/api/tasks/${taskIDs[0]}`, "PATCH", { status: "done" });
  await evaluate("window.dispatchEvent(new Event('focus'))");
  await until(() => evaluate(`${panel}?.querySelectorAll('li').length === 1`));
  await request(`/api/tasks/${taskIDs[1]}`, "PATCH", { due_date: today });
  await evaluate("window.dispatchEvent(new Event('focus'))");
  await until(() => evaluate("window.delivered.length === 1"));
  assert.equal(await evaluate("window.delivered[0].title"), "Akhflow: Срок сегодня");
  console.log("PASS: reload deduplication, completed task removed, changed deadline triggers reminder");
  const texts = [await evaluate(`${panel}.innerText`)];
  await cdp("Browser.resetPermissions");
  await evaluate("window.dispatchEvent(new Event('focus'))");
  await until(() => evaluate(`${panel}?.innerText.includes('Включить уведомления браузера')`));
  texts.push(await evaluate(`${panel}.innerText`));
  await evaluate("Object.defineProperty(Notification, 'permission', { get: () => 'denied' }); window.dispatchEvent(new Event('focus'))");
  await until(() => evaluate(`${panel}?.innerText.includes('разрешение в настройках сайта')`));
  assert.equal(await evaluate(`${panel}.querySelectorAll('li').length`), 1);
  assert.equal(await evaluate("window.delivered.length"), 1);
  texts.push(await evaluate(`${panel}.innerText`));
  await evaluate("delete window.Notification; [...document.querySelectorAll('button')].find(button => button.textContent.includes('Список')).click()");
  await until(() => evaluate(`${panel}?.innerText.includes('Уведомления браузера недоступны')`));
  assert.equal(await evaluate(`${panel}.querySelectorAll('li').length`), 1);
  texts.push(await evaluate(`${panel}.innerText`));
  console.log("PASS: default/denied/unsupported browser states preserve UI reminders");
  const emptySlug = `${slug}-empty`;
  await request("/api/projects", "POST", { slug: emptySlug, name: "Smoke empty deadlines" });
  await cdp("Page.reload");
  await until(() => evaluate("document.querySelector('select')?.options.length === 2"));
  await evaluate(`const select = document.querySelector('select'); select.value = ${JSON.stringify(emptySlug)}; select.dispatchEvent(new Event('change', { bubbles: true }));`);
  await until(() => evaluate(`${panel}?.innerText.includes('Нет задач со сроком сегодня или завтра')`));
  assert.equal(await evaluate(`${panel}.querySelectorAll('li').length`), 0);
  texts.push(await evaluate(`${panel}.innerText`));
  console.log("PASS: switching projects clears previous reminders");
  const textFile = join(profile, "ui.md");
  await writeFile(textFile, texts.join("\n\n"));
  if (process.env.TEXT_LINT_SCRIPT) {
    const lint = spawnSync(process.env.PYTHON_BIN ?? "python3", [
      process.env.TEXT_LINT_SCRIPT, textFile,
    ], { stdio: "inherit" });
    assert.equal(lint.status, 0, "UI text lint failed");
  } else {
    console.log("SKIP: UI text lint (set TEXT_LINT_SCRIPT to enable)");
  }
} finally {
  socket?.close();
  chrome.kill();
  for (const id of taskIDs) {
    await request(`/api/tasks/${id}`, "DELETE");
  }
  await rm(profile, { recursive: true, force: true, maxRetries: 3, retryDelay: 100 });
}
