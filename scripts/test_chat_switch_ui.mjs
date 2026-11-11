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
  let releaseFirstLoad;
  const firstLoad = new Promise((resolve) => { releaseFirstLoad = resolve; });
  let releaseAnswer;
  const answer = new Promise((resolve) => { releaseAnswer = resolve; });
  let firstLoadStarted;
  const loadStarted = new Promise((resolve) => { firstLoadStarted = resolve; });
  let answerStarted;
  const postStarted = new Promise((resolve) => { answerStarted = resolve; });
  let firstChatLoads = 0;
  const chatOne = [{ id: 11, role: "agent", content: "История первого" }];
  const chatTwo = [{ id: 21, role: "agent", content: "История второго" }];
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let data = [];
    if (path === "/api/projects") {
      data = [{ id: 1, slug: "alpha", name: "Альфа" }];
    } else if (path === "/api/projects/alpha/chats") {
      data = [{ id: 1, title: "Первый чат" }, { id: 2, title: "Второй чат" }];
    } else if (path === "/api/chats/1/messages" && request.method() === "GET") {
      firstChatLoads++;
      if (firstChatLoads === 1) {
        firstLoadStarted();
        await firstLoad;
        data = [{ id: 10, role: "agent", content: "Устаревшая история" }];
      } else {
        data = chatOne;
      }
    } else if (path === "/api/chats/2/messages") {
      data = chatTwo;
    } else if (path === "/api/chats/1/messages" && request.method() === "POST") {
      answerStarted();
      await answer;
      chatOne.push({ id: 12, role: "user", content: "Вопрос первому" });
      chatOne.push({ id: 13, role: "agent", content: "Ответ первому" });
      data = { reply: "Ответ первому", tool_calls: null, iterations: 1 };
    }
    await route.fulfill({ status: 200, json: data });
  });
  await page.goto(url);
  await page.getByRole("button", { name: "Агент", exact: true }).click();
  await loadStarted;
  await page.getByRole("button", { name: "Второй чат" }).click();
  await page.getByText("История второго").waitFor();
  await page.getByRole("button", { name: "Первый чат" }).click();
  await page.getByText("История первого").waitFor();
  releaseFirstLoad();
  await delay(200);
  assert.equal(await page.getByText("Устаревшая история").count(), 0);
  const input = page.getByPlaceholder("Скажи агенту, что сделать...");
  await input.fill("Вопрос первому");
  await input.press("Enter");
  await postStarted;
  await page.getByRole("button", { name: "Второй чат" }).click();
  await page.getByText("История второго").waitFor();
  await page.getByRole("button", { name: "Первый чат" }).click();
  await page.getByText("Вопрос первому").waitFor();
  releaseAnswer();
  await page.getByText("Ответ первому").waitFor();
  await page.getByRole("button", { name: "Второй чат" }).click();
  await page.getByText("История второго").waitFor();
  assert.equal(await page.getByText("Ответ первому").count(), 0);
  await page.getByRole("button", { name: "Первый чат" }).click();
  await page.getByText("Ответ первому").waitFor();
  console.log("PASS: delayed chat history and agent replies stay in their original chat");
} finally {
  if (browser) {
    await browser.close();
  }
  server.kill();
}
