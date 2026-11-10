#!/usr/bin/env python3
"""backlog_search.py — быстрый поиск и фильтрация задач проекта Akhflow.

Одна выборка задач через REST + локальная фильтрация (без N+1 запросов),
с подтягиванием названий эпиков и имён исполнителей.

Примеры:
  ./scripts/backlog_search.py
  ./scripts/backlog_search.py --status backlog --sort priority
  ./scripts/backlog_search.py --text импорт --status todo,in_progress
  ./scripts/backlog_search.py --priority high --epic 2
  ./scripts/backlog_search.py --overdue
  ./scripts/backlog_search.py --assignee none --no-due
  ./scripts/backlog_search.py --json --limit 5
  ./scripts/backlog_search.py --count

Требования: python3, запущенный API (./bin/akhflow serve api или make up),
AKHFLOW_TOKEN в окружении или .env (в корне репозитория).
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import sys
import urllib.error
import urllib.request
from collections import Counter
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
ENV_FILE = REPO / ".env"

STATUS_ORDER = ["backlog", "todo", "in_progress", "done"]
PRIO_ORDER = {"high": 0, "medium": 1, "low": 2, "": 3}


def load_env() -> dict[str, str]:
    env = dict(os.environ)
    if ENV_FILE.exists():
        for line in ENV_FILE.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, value = line.split("=", 1)
            env.setdefault(key.strip(), value.strip())
    return env


def api_get(base: str, token: str, path: str):
    req = urllib.request.Request(base + path, headers={"Authorization": f"Bearer {token}"})
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.load(resp)
    except urllib.error.HTTPError as exc:
        if exc.code == 401:
            sys.exit("401: токен из .env устарел — обнови AKHFLOW_TOKEN (или перелогинься в web)")
        body = exc.read().decode("utf-8", "replace")[:200]
        sys.exit(f"HTTP {exc.code} {path}: {body}")
    except urllib.error.URLError as exc:
        sys.exit(
            f"API недоступен ({base}): {exc.reason}. "
            "Подними API: ./bin/akhflow serve api (или make up)"
        )


def parse_csv(raw: str | None) -> list[str]:
    return [item.strip() for item in (raw or "").split(",") if item.strip()]


def iso_date(task: dict) -> dt.date | None:
    raw = task.get("due_date")
    if not raw:
        return None
    try:
        return dt.datetime.fromisoformat(raw.replace("Z", "+00:00")).date()
    except ValueError:
        return None


def main() -> None:
    ap = argparse.ArgumentParser(
        description="Быстрый поиск по задачам Akhflow (REST + локальные фильтры).",
        epilog=(
            "Статусы: backlog, todo, in_progress, done (можно 'open' = backlog,todo,in_progress). "
            "В списке API нет recurring-мастеров (recurring != '') — для них используй psql "
            "(см. references/queries.md в skill backlog-search)."
        ),
    )
    ap.add_argument("-p", "--project", default="akhflow", help="slug проекта (по умолчанию akhflow)")
    ap.add_argument("--status", help="backlog,todo,in_progress,done или open")
    ap.add_argument("--priority", help="high,medium,low (через запятую)")
    ap.add_argument("--label", help="метки через запятую (достаточно любой)")
    ap.add_argument("--epic", help="id эпика или подстрока названия")
    ap.add_argument("--assignee", help="id/email/имя исполнителя; 'none' — без исполнителя")
    ap.add_argument("--text", help="подстрока в title или description (без регистра)")
    ap.add_argument("--overdue", action="store_true", help="просроченные: due < сегодня и статус != done")
    ap.add_argument("--no-due", action="store_true", help="только задачи без дедлайна")
    ap.add_argument("--due-before", help="дедлайн не позже YYYY-MM-DD")
    ap.add_argument("--due-after", help="дедлайн не раньше YYYY-MM-DD")
    ap.add_argument("--sort", choices=["id", "priority", "due", "status", "updated"], default="id")
    ap.add_argument("--desc", action="store_true", help="обратный порядок сортировки")
    ap.add_argument("--limit", type=int, default=0, help="максимум строк (0 = все)")
    ap.add_argument("--json", action="store_true", help="выдать отфильтрованные задачи в JSON")
    ap.add_argument("--count", action="store_true", help="только сводка по статусам")
    args = ap.parse_args()

    env = load_env()
    base = env.get("AKHFLOW_API", "http://localhost:8080").rstrip("/")
    token = env.get("AKHFLOW_TOKEN", "")
    if not token:
        sys.exit("AKHFLOW_TOKEN не найден в окружении или .env")

    tasks = api_get(base, token, f"/api/projects/{args.project}/tasks")
    epics = {e["id"]: e["title"] for e in api_get(base, token, f"/api/projects/{args.project}/epics")}
    members = {
        m["id"]: (m.get("display_name") or m.get("email") or f"user#{m['id']}")
        for m in api_get(base, token, f"/api/projects/{args.project}/members")
    }

    statuses = set(parse_csv(args.status))
    if "open" in statuses:
        statuses.discard("open")
        statuses |= {"backlog", "todo", "in_progress"}
    priorities = set(parse_csv(args.priority))
    labels = set(parse_csv(args.label))
    today = dt.date.today()

    def assignee_match(task: dict) -> bool:
        want = (args.assignee or "").strip()
        assignee_id = task.get("assignee_id")
        if want.lower() in ("none", "unassigned", "-"):
            return assignee_id is None
        if assignee_id is None:
            return False
        if want.isdigit():
            return assignee_id == int(want)
        name = (members.get(assignee_id) or "").lower()
        return want.lower() in name

    def epic_match(task: dict) -> bool:
        want = (args.epic or "").strip()
        epic_id = task.get("epic_id")
        if want.isdigit():
            return epic_id == int(want)
        if epic_id is None:
            return False
        return want.lower() in (epics.get(epic_id) or "").lower()

    def text_match(task: dict) -> bool:
        needle = (args.text or "").strip().lower()
        return needle in (task.get("title") or "").lower() or needle in (task.get("description") or "").lower()

    before = dt.date.fromisoformat(args.due_before) if args.due_before else None
    after = dt.date.fromisoformat(args.due_after) if args.due_after else None

    def keep(task: dict) -> bool:
        if statuses and task["status"] not in statuses:
            return False
        if priorities and task.get("priority", "") not in priorities:
            return False
        if labels and not labels.intersection(task.get("labels") or []):
            return False
        if args.epic and not epic_match(task):
            return False
        if args.assignee and not assignee_match(task):
            return False
        if args.text and not text_match(task):
            return False
        due = iso_date(task)
        if args.overdue and (due is None or due >= today or task["status"] == "done"):
            return False
        if args.no_due and due is not None:
            return False
        if before and (due is None or due > before):
            return False
        if after and (due is None or due < after):
            return False
        return True

    rows = [task for task in tasks if keep(task)]

    def sort_key(task: dict):
        if args.sort == "priority":
            return (PRIO_ORDER.get(task.get("priority", ""), 9), task["id"])
        if args.sort == "due":
            due = iso_date(task)
            return (due is None, due or today, task["id"])
        if args.sort == "status":
            return (STATUS_ORDER.index(task["status"]) if task["status"] in STATUS_ORDER else 9, task["id"])
        if args.sort == "updated":
            return (task.get("updated_at") or "", task["id"])
        return task["id"]

    rows.sort(key=sort_key, reverse=args.desc)
    if args.limit > 0:
        rows = rows[: args.limit]

    counts = Counter(task["status"] for task in rows)
    summary = "  ".join(f"{status}={counts[status]}" for status in STATUS_ORDER if counts.get(status))
    summary_line = f"{len(rows)} задач | {summary}" if rows else "Ничего не найдено."

    if args.json:
        enriched = [
            {
                **task,
                "epic_title": epics.get(task.get("epic_id")),
                "assignee_name": members.get(task.get("assignee_id")),
            }
            for task in rows
        ]
        print(json.dumps(enriched, ensure_ascii=False, indent=1))
        return

    print(summary_line)
    if args.count or not rows:
        if rows and counts.get("done"):
            print(f"(включая done={counts['done']}; обычно done исключают: --status open или --status backlog,todo,in_progress)")
        return

    table = []
    for task in rows:
        due = iso_date(task)
        overdue = bool(due and due < today and task["status"] != "done")
        table.append(
            [
                f"#{task['id']}",
                task["status"],
                (task.get("priority") or "—"),
                (epics.get(task.get("epic_id")) or "—"),
                (members.get(task.get("assignee_id")) or "—"),
                ((due.isoformat() + (" !" if overdue else "")) if due else "—"),
                task["title"],
            ]
        )

    headers = ["ID", "STATUS", "PRIO", "EPIC", "ASSIGNEE", "DUE", "TITLE"]
    caps = [8, 12, 6, 26, 16, 12, 70]
    widths = [len(h) for h in headers]
    for row in table:
        for i, cell in enumerate(row):
            widths[i] = min(caps[i], max(widths[i], len(cell)))
    fmt = "  ".join(f"{{:<{width}}}" for width in widths)
    print(fmt.format(*headers))
    print("  ".join("-" * width for width in widths))
    for row in table:
        cells = [(cell[: caps[i] - 1] + "…") if len(cell) > caps[i] else cell for i, cell in enumerate(row)]
        print(fmt.format(*cells))
    if counts.get("done"):
        print(f"(done={counts['done']}; чтобы исключить: --status open)")


if __name__ == "__main__":
    main()
