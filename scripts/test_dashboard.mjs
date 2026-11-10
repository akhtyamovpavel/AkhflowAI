import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import ts from "../web/node_modules/typescript/lib/typescript.js";

const source = await readFile(new URL("../web/src/lib/dashboard.ts", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
});
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled.outputText).toString("base64")}`;
const { summarizeDashboard } = await import(moduleURL);

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
