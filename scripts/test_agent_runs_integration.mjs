// Build the backend binary first, then provide a disposable PostgreSQL connection:
// TEST_DATABASE_URL=... TEST_API_BINARY=bin/agent-runs-test node scripts/test_agent_runs_integration.mjs
// The database user must be allowed to create and drop the test's own schema.
import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import http from "node:http";
import { setTimeout as delay } from "node:timers/promises";
import { pathToFileURL } from "node:url";

const dsn = process.env.TEST_DATABASE_URL;
const binary = process.env.TEST_API_BINARY;
if (!dsn || !binary) throw new Error("Set TEST_DATABASE_URL and TEST_API_BINARY for an isolated integration test");
const database = new URL(dsn);
if (!["postgres:", "postgresql:"].includes(database.protocol)) throw new Error("PostgreSQL URL required");
const moduleName = process.env.PLAYWRIGHT_MODULE;
const defaultModule = new URL("../web/node_modules/playwright-core/index.mjs", import.meta.url).href;
const playwright = await import(moduleName ? pathToFileURL(moduleName).href : defaultModule);
const schema = `akhflow_test_${randomBytes(12).toString("hex")}`;
const psqlEnv = { ...process.env };
psqlEnv.PGHOST = database.hostname;
psqlEnv.PGPORT = database.port || "5432";
psqlEnv.PGUSER = decodeURIComponent(database.username);
psqlEnv.PGPASSWORD = decodeURIComponent(database.password);
psqlEnv.PGDATABASE = decodeURIComponent(database.pathname.slice(1));
psqlEnv.PGSSLMODE = database.searchParams.get("sslmode") || "prefer";
const sql = (query) => {
  const result = spawnSync("psql", ["-X", "-q", "-v", "ON_ERROR_STOP=1", "-c", query], { env: psqlEnv, stdio: "ignore" });
  if (result.status !== 0) throw new Error("Test schema operation failed");
};
const listen = async (server) => {
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  return server.address().port;
};
const reservePort = async () => {
  const server = http.createServer();
  const port = await listen(server);
  await new Promise((resolve) => server.close(resolve));
  return port;
};
const stop = async (processHandle) => {
  if (!processHandle || processHandle.exitCode !== null) return;
  processHandle.kill("SIGTERM");
  await Promise.race([new Promise((resolve) => processHandle.once("exit", resolve)), delay(5000)]);
  if (processHandle.exitCode === null) processHandle.kill("SIGKILL");
};
let api;
let vite;
let browser;
let proxy;
let model;
let schemaCreated = false;
let releaseModel;
try {
  sql(`CREATE SCHEMA "${schema}"`);
  schemaCreated = true;
  database.searchParams.set("search_path", schema);
  let modelEntered = 0;
  let modelResponse;
  model = http.createServer((request, response) => {
    modelEntered++;
    modelResponse = response;
    releaseModel = () => {
      if (response.destroyed) return;
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify({ choices: [{ message: { role: "assistant", content: "Ответ настоящего сервера" } }] }));
    };
    request.resume();
  });
  const modelPort = await listen(model);
  const apiPort = await reservePort();
  const vitePort = await reservePort();
  const apiEnv = { PATH: process.env.PATH, DATABASE_URL: database.toString(), JWT_SECRET: "integration-test-secret", API_PORT: String(apiPort), OLLAMA_HOST: `http://127.0.0.1:${modelPort}`, AGENT_MODEL: "fixture" };
  api = spawn(binary, ["serve", "api"], { env: apiEnv, stdio: "ignore" });
  vite = spawn("./node_modules/.bin/vite", ["--host", "127.0.0.1", "--port", String(vitePort), "--strictPort"], { cwd: new URL("../web", import.meta.url), stdio: "ignore" });
  proxy = http.createServer((request, response) => {
    const port = request.url.startsWith("/api/") ? apiPort : vitePort;
    const upstream = http.request({ hostname: "127.0.0.1", port, path: request.url, method: request.method, headers: request.headers }, (incoming) => {
      response.writeHead(incoming.statusCode, incoming.headers);
      incoming.pipe(response);
    });
    upstream.on("error", () => response.destroy());
    request.pipe(upstream);
  });
  const proxyPort = await listen(proxy);
  const origin = `http://127.0.0.1:${proxyPort}`;
  let ready = false;
  for (let attempt = 0; attempt < 150; attempt++) {
    try {
      const [web, backend] = await Promise.all([fetch(origin), fetch(`${origin}/api/projects`)]);
      if (web.ok && backend.status === 401) {
        ready = true;
        break;
      }
    } catch {
      await delay(100);
    }
  }
  assert.ok(ready, "test API and UI did not start");
  const call = async (path, options = {}, token = "") => {
    const headers = { "content-type": "application/json" };
    if (token) headers.authorization = `Bearer ${token}`;
    const result = await fetch(`${origin}${path}`, { ...options, headers });
    assert.ok(result.ok, `fixture request failed: ${path} (${result.status})`);
    return result.json();
  };
  const account = await call("/api/auth/register", { method: "POST", body: JSON.stringify({ email: "smoke@example.invalid", password: "fixture-password" }) });
  await call("/api/projects", { method: "POST", body: JSON.stringify({ name: "Smoke", slug: "smoke" }) }, account.token);
  await call("/api/projects/smoke/chats", { method: "POST", body: "{}" }, account.token);
  browser = await playwright.chromium.launch({ executablePath: "/usr/bin/google-chrome", headless: true });
  const page = await browser.newPage();
  await page.addInitScript((token) => {
    localStorage.setItem("akhflow.token", token);
    localStorage.setItem("akhflow.project", "smoke");
  }, account.token);
  await page.goto(origin);
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  const input = page.getByPlaceholder("Скажи агенту, что сделать...");
  await input.fill("Проверь работу агента");
  await input.press("Enter");
  for (let attempt = 0; modelEntered < 1 && attempt < 150; attempt++) await delay(100);
  assert.equal(modelEntered, 1, "real worker did not call the fixture model");
  await page.reload();
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await page.getByText("Агент работает", { exact: false }).waitFor();
  releaseModel();
  await page.getByText("Ответ настоящего сервера").waitFor({ timeout: 20000 });
  await page.getByText("Выполнено", { exact: true }).waitFor({ timeout: 10000 });
  await input.fill("Останови выполнение");
  await input.press("Enter");
  for (let attempt = 0; modelEntered < 2 && attempt < 150; attempt++) await delay(100);
  assert.equal(modelEntered, 2);
  await page.getByRole("button", { name: "Остановить" }).click();
  await page.getByText("Отменено", { exact: true }).waitFor({ timeout: 20000 });
  assert.equal(modelEntered, 2, "reload or cancellation must not duplicate the run");
  console.log("PASS: real API, isolated database, background recovery and cancellation");
  if (modelResponse && !modelResponse.destroyed) modelResponse.destroy();
} finally {
  if (releaseModel) releaseModel();
  if (browser) await browser.close();
  await stop(vite);
  await stop(api);
  if (proxy) proxy.closeAllConnections();
  if (proxy) await new Promise((resolve) => proxy.close(resolve));
  if (model) model.closeAllConnections();
  if (model) await new Promise((resolve) => model.close(resolve));
  if (schemaCreated) sql(`DROP SCHEMA "${schema}" CASCADE`);
}
