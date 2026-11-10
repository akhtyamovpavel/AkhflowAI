import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import ts from "../web/node_modules/typescript/lib/typescript.js";

const source = await readFile(new URL("../web/src/lib/dashboard.ts", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
});
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled.outputText).toString("base64")}`;
const { summarizeDashboard, summarizeEpics } = await import(moduleURL);

const tasks = [
  { status: "backlog", priority: "high", assignee_id: 1 },
  { status: "todo", priority: "medium", assignee_id: 1 },
  { status: "done", priority: "low", assignee_id: null },
  { status: "in_progress", priority: "high" },
  { status: "external", priority: "unknown", assignee_id: 9 },
];
const members = [
  { id: 1, display_name: "Анна" },
  { id: 2, display_name: "Иван" },
];
const summary = summarizeDashboard(tasks, members);
const counts = (rows) => Object.fromEntries(rows.map((row) => [row.id, row.count]));
assert.equal(summary.total, 5);
assert.deepEqual(counts(summary.statuses), {
  backlog: 1, todo: 1, in_progress: 1, done: 1, external: 1,
});
assert.deepEqual(counts(summary.priorities), { high: 2, medium: 1, low: 1, unknown: 1 });
assert.deepEqual(counts(summary.assignees), { 1: 2, 2: 0, 9: 1, unassigned: 2 });
assert.equal(summary.assignees.find((row) => row.id === "9").label, "Пользователь #9");
const empty = summarizeDashboard([], []);
assert.equal(empty.total, 0);
assert.deepEqual(counts(empty.assignees), { unassigned: 0 });
assert.ok(empty.statuses.every((row) => row.count === 0));
assert.ok(empty.priorities.every((row) => row.count === 0));
console.log("PASS: dashboard totals, categories, missing assignees and empty project");

const epics = [
  { id: 1, title: "Первый" },
  { id: 2, title: "Пустой" },
  { id: 3, title: "Завершённый" },
];
const epicTasks = [
  { epic_id: 1, status: "done" },
  { epic_id: 1, status: "todo" },
  { epic_id: 1, status: "in_progress" },
  { epic_id: 3, status: "done" },
  { epic_id: null, status: "done" },
  { epic_id: 99, status: "done" },
];
assert.deepEqual(summarizeEpics(epicTasks, epics), [
  { id: 1, title: "Первый", done: 1, total: 3, percent: 33 },
  { id: 2, title: "Пустой", done: 0, total: 0, percent: 0 },
  { id: 3, title: "Завершённый", done: 1, total: 1, percent: 100 },
]);
assert.deepEqual(summarizeEpics(epicTasks, []), []);
console.log("PASS: epic completion, empty epics, unrelated tasks and rounding");
