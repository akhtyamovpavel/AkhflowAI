#!/usr/bin/env python3
"""verify_quick_add.py — e2e-проверка: «Новая задача...» создаёт задачу в Backlog.

Запуск из корня репозитория при поднятом стеке (при необходимости):
  make up-db && ./bin/akhflow serve api      # API :8080
  cd web && npm run dev                       # web :5173 (проксирует /api)
  python3 e2e/verify_quick_add.py

Что делает: открывает UI in headless Chrome через Playwright MCP, подставляет JWT
из .env, переключается на песочницу smoke-deps, создаёт задачу через поле
«Новая задача...», проверяет:
  1) колонку Backlog в DOM,
  2) статус backlog через REST API,
сохраняет скриншот в e2e/artifacts/ и удаляет тестовую задачу.

Использует @playwright/mcp@0.0.83: сначала ищет установленный CLI в ~/.npm/_npx,
иначе `npx -y @playwright/mcp@0.0.83`. Exit code: 0 = PASS, 1 = FAIL.
"""

from __future__ import annotations

import base64
import glob
import json
import os
import re
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
ARTIFACTS = REPO / "e2e" / "artifacts"
CHECK = REPO / ".e2e-check"
SANDBOX_PROJECT = "smoke-deps"
TIMEOUT_SECONDS = 300


def load_env() -> dict[str, str]:
    env = dict(os.environ)
    env_file = REPO / ".env"
    if env_file.exists():
        for line in env_file.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, value = line.split("=", 1)
            env.setdefault(key.strip(), value.strip())
    return env


def http_json(base: str, token: str, method: str, path: str, body: dict | None = None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method)
    req.add_header("Authorization", f"Bearer {token}")
    if data is not None:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=10) as resp:
        raw = resp.read().decode("utf-8", "replace")
        return json.loads(raw) if raw.strip() else {}


def find_mcp_command() -> list[str]:
    candidates = sorted(glob.glob(str(Path.home() / ".npm/_npx/*/node_modules/@playwright/mcp/cli.js")))
    if candidates:
        return ["node", candidates[0]]
    return ["npx", "-y", "@playwright/mcp@0.0.83"]


class Mcp:
    def __init__(self, proc: subprocess.Popen):
        self.proc = proc
        self.next_id = 0

    def _send(self, msg: dict) -> None:
        assert self.proc.stdin is not None
        self.proc.stdin.write(json.dumps(msg) + "\n")
        self.proc.stdin.flush()

    def _recv(self) -> dict:
        assert self.proc.stdout is not None
        while True:
            line = self.proc.stdout.readline()
            if not line:
                raise RuntimeError("Playwright MCP завершился неожиданно")
            line = line.strip()
            if line:
                return json.loads(line)

    def initialize(self) -> dict:
        self._send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
            "protocolVersion": "2025-06-18", "capabilities": {},
            "clientInfo": {"name": "verify-quick-add", "version": "1"}}})
        while True:
            msg = self._recv()
            if msg.get("id") == 1:
                break
        self._send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        return msg

    def call(self, name: str, arguments: dict) -> dict:
        self.next_id += 1
        call_id = self.next_id
        self._send({"jsonrpc": "2.0", "id": call_id, "method": "tools/call",
                    "params": {"name": name, "arguments": arguments}})
        while True:
            msg = self._recv()
            if msg.get("id") == call_id:
                break
        if "error" in msg:
            raise RuntimeError(f"{name}: {msg['error']}")
        result = msg.get("result") or {}
        if result.get("isError"):
            raise RuntimeError(f"{name}: {self.result_text(result)}")
        return result

    @staticmethod
    def result_text(result: dict) -> str:
        return "\n".join(item.get("text", "") for item in result.get("content", [])
                         if item.get("type") == "text")

    def eval_json(self, function: str):
        text = self.result_text(self.call("browser_evaluate", {"function": function}))
        match = re.search(r"### Result\n(.*?)\n### Ran Playwright code", text, re.S)
        payload = match.group(1) if match else text
        try:
            return json.loads(payload)
        except json.JSONDecodeError:
            return payload

    def screenshot(self, dest: Path) -> None:
        result = self.call("browser_take_screenshot", {"type": "png"})
        images = [item for item in result.get("content", []) if item.get("type") == "image"]
        if not images:
            raise RuntimeError("screenshot: в ответе нет image-контента")
        dest.write_bytes(base64.b64decode(images[0]["data"]))


def fail(message: str) -> None:
    print(f"FAIL: {message}", file=sys.stderr)
    raise SystemExit(1)


def main() -> None:
    env = load_env()
    base = env.get("AKHFLOW_API", "http://localhost:8080").rstrip("/")
    token = env.get("AKHFLOW_TOKEN", "")
    if not token:
        fail("AKHFLOW_TOKEN не найден в .env")

    try:
        health = http_json(base, token, "GET", "/api/health")
    except Exception as exc:  # noqa: BLE001
        fail(f"API недоступен ({base}): {exc}")
    if health.get("status") != "ok":
        fail(f"API health не ok: {health}")

    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    for sub in ("home", "npm-cache", "npm-logs", "pw-browsers"):
        (CHECK / sub).mkdir(parents=True, exist_ok=True)
    child_env = {
        **os.environ,
        "HOME": str(CHECK / "home"),
        "npm_config_cache": str(CHECK / "npm-cache"),
        "npm_config_logs_dir": str(CHECK / "npm-logs"),
        "PLAYWRIGHT_BROWSERS_PATH": str(CHECK / "pw-browsers"),
    }
    cmd = find_mcp_command() + [
        "--headless", "--browser", "chrome", "--isolated",
        "--output-dir", str(ARTIFACTS), "--image-responses", "allow",
    ]
    proc = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                            stderr=subprocess.DEVNULL, text=True, bufsize=1,
                            cwd=REPO, env=child_env)
    signal.alarm(TIMEOUT_SECONDS)
    title = f"verify-quick-add-{int(time.time())}"
    created_id: int | None = None
    try:
        mcp = Mcp(proc)
        info = mcp.initialize()["result"]["serverInfo"]
        print(f"Playwright MCP: {info.get('name')} {info.get('version')}")

        mcp.call("browser_navigate", {"url": "http://localhost:5173/"})
        mcp.eval_json(
            "() => { localStorage.setItem('akhflow.token', " + json.dumps(token) + ");"
            " localStorage.setItem('akhflow.project', " + json.dumps(SANDBOX_PROJECT) + ");"
            " return 'ok'; }")
        mcp.call("browser_navigate", {"url": "http://localhost:5173/"})
        mcp.call("browser_wait_for", {"text": "Добавить"})

        mcp.call("browser_type", {
            "target": 'input[placeholder="Новая задача..."]',
            "text": title,
        })
        mcp.call("browser_press_key", {"key": "Enter"})
        mcp.call("browser_wait_for", {"text": title})

        columns = mcp.eval_json(
            "() => { const title = " + json.dumps(title) + ";"
            " const names = ['backlog','todo','in progress','done'];"
            " const heads = {};"
            " for (const name of names) {"
            "   const el = [...document.querySelectorAll('div,span,h1,h2,h3,h4')]"
            "     .find(x => x.children.length === 0 && (x.textContent||'').trim().toLowerCase() === name);"
            "   if (el) heads[name] = el;"
            " }"
            " const out = {};"
            " for (const [name, head] of Object.entries(heads)) {"
            "   let node = head.parentElement, inColumn = false;"
            "   while (node) {"
            "     const others = names.filter(n => n !== name && heads[n] && node.contains(heads[n]));"
            "     if (others.length > 0) break;"
            "     if ((node.textContent||'').includes(title)) { inColumn = true; break; }"
            "     node = node.parentElement;"
            "   }"
            "   out[name] = inColumn;"
            " }"
            " return out; }")
        print(f"DOM-колонки: {columns}")
        mcp.screenshot(ARTIFACTS / "verify-quick-add.png")

        tasks = http_json(base, token, "GET", f"/api/projects/{SANDBOX_PROJECT}/tasks")
        matches = [task for task in tasks if task.get("title") == title]
        if not matches:
            fail("задача создана в UI, но не найдена через API")
        task = matches[0]
        created_id = task["id"]
        print(f"API: task #{created_id} status={task['status']} priority={task['priority']}")
        if task["status"] != "backlog":
            fail(f"ожидался status=backlog, получен {task['status']}")
        if columns.get("backlog") is not True:
            fail(f"в DOM задача не в колонке Backlog: {columns}")
        print(f"PASS: «{title}» создана в Backlog (task #{created_id})")
    finally:
        signal.alarm(0)
        if created_id is not None:
            try:
                http_json(base, token, "DELETE", f"/api/tasks/{created_id}")
                print(f"cleanup: task #{created_id} удалена")
            except Exception as exc:  # noqa: BLE001
                print(f"WARN: не удалось удалить task #{created_id}: {exc}", file=sys.stderr)
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()


if __name__ == "__main__":
    main()
