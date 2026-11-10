---
name: backlog-search
description: "Efficient search over the Akhflow tracker backlog (project akhflow): one fetch plus local filters, or read-only SQL, instead of N+1 tracker calls. Use when asked what is in the backlog, to find/filter/count/sort tasks by status, priority, epic, assignee, label, text or due date, to pick the next task, or to report overdue/stalled work. Триггеры: бэклог, backlog, «что у нас в задачах», фильтр по статусу/приоритету/эпику/исполнителю/метке, просрочено, «что взять дальше», сводка по задачам."
whenToUse: "Пользователь спрашивает про бэклог/задачи/фильтры/приоритеты/просрочки/эпики/загрузку исполнителей или какую задачу взять следующей."
---

# Backlog search (Akhflow)

Главное правило: **одна выборка + локальная фильтрация**. В проекте `akhflow` ~30–50 задач,
полный список — это один REST-вызов (<100 мс). Не делай N+1: `get_task` на каждую задачу
тянет ещё comments и history — это в 10× дороже по времени и токенам.

Порядок предпочтения путей:
1. `scripts/backlog_search.py` — одна выборка, все фильтры локально (для любых задач).
2. CLI `task list` — когда нужен только срез по статусу и человекочитаемая таблица.
3. MCP `list_tasks` — когда работаешь в DSH-сессии с akhflow MCP (агент akhflow-solve).
4. psql (read-only) — агрегаты, recurring-мастера, зависимости (`references/queries.md`).

## 1. Быстрый путь: scripts/backlog_search.py

Запускать из корня репозитория. Читает `AKHFLOW_API`/`AKHFLOW_TOKEN` из окружения или `.env`,
подтягивает названия эпиков и имена исполнителей, печатает сводку + таблицу
`ID STATUS PRIO EPIC ASSIGNEE DUE TITLE` (`!` — просроченный дедлайн).

```bash
./scripts/backlog_search.py                                # все задачи, включая done
./scripts/backlog_search.py --status open --sort priority   # незакрытые, high → medium → low
./scripts/backlog_search.py --status backlog                 # только бэклог
./scripts/backlog_search.py --text импорт --status todo,in_progress
./scripts/backlog_search.py --priority high --epic 2         # эпик по id или по подстроке названия
./scripts/backlog_search.py --overdue                        # due < сегодня и статус != done
./scripts/backlog_search.py --assignee none --no-due         # без исполнителя и без дедлайна
./scripts/backlog_search.py --label feature --status open
./scripts/backlog_search.py --due-before 2026-10-31
./scripts/backlog_search.py --json --limit 5                 # JSON для программной обработки
./scripts/backlog_search.py --count                          # только сводка по статусам
```

Ключи: `-p/--project` (default `akhflow`), `--status`, `--priority`, `--label`, `--epic`,
`--assignee` (id / email / имя / `none`), `--text`, `--overdue`, `--no-due`, `--due-before`,
`--due-after`, `--sort id|priority|due|status|updated`, `--desc`, `--limit`, `--json`, `--count`.
`--status open` = `backlog,todo,in_progress`.

## 2. Без скрипта: CLI, REST, MCP

**CLI** (нужен поднятый API; выводит только ID/STATUS/PRIORITY/TITLE):
```bash
./bin/akhflow task list -p akhflow --status backlog
./bin/akhflow task get 46          # одна задача: labels, эпик, зависимости, история
```

**REST** — единственный серверный фильтр `?status=`; всё остальное фильтруй локально.
`GET /api/projects/{slug}/tasks[?status=...]`, `GET /api/projects/{slug}/epics`,
`GET /api/projects/{slug}/members`. Один curl + python3 (jq может отсутствовать):
```bash
TOKEN=$(grep -o 'AKHFLOW_TOKEN=.*' .env | cut -d= -f2)
curl -s "http://localhost:8080/api/projects/akhflow/tasks" -H "Authorization: Bearer $TOKEN" \
  | python3 -c 'import json,sys
tasks=json.load(sys.stdin)
for t in tasks:
    if t["status"] in ("todo","in_progress") and t["priority"]=="high":
        print(f"#{t[\"id\"]} [{t[\"status\"]}] {t[\"title\"]} labels={t.get(\"labels\")} due={t.get(\"due_date\")}")'
```
Задача в списке уже содержит `labels`, `depends_on`, `blocks`, `epic_id`, `assignee_id`,
`due_date`, `estimate_min` — `get_task` для деталей нужен только выбранным кандидатам.

**MCP** (DSH-сессии с akhflow MCP, например агент `akhflow-solve`):
- `mcp__akhflow__list_tasks` с `project: "akhflow"`, `brief: true` — один раз на поиск;
  статус фильтруется параметром `status`. `brief: false` — если нужны labels/due/estimate.
- `mcp__akhflow__list_epics` — один раз; `mcp__akhflow__get_task` — только для 1–3 кандидатов.
- Не вызывай `get_task` по всем задачам списка: это лишние comments+history в контексте.

**psql** (read-only, для агрегатов и полного доступа): см. `references/queries.md`.
Списки REST/CLI/MCP скрывают recurring-мастера — их видно только в SQL.

## 3. Данные и ловушки

- Статусы: `backlog → todo → in_progress → done`; приоритеты `low/medium/high` (high > medium > low).
- **`done` не исключается по умолчанию** — всегда фильтруй `--status open` или явным списком.
- **Просрочено** = `due_date::date < today AND status != 'done'`; done никогда не просрочен.
- **Recurring-мастера** (`recurring != ''`) скрыты из REST/CLI/MCP-списков; их экземпляры
  (например «Ежедневная проверка») в списке есть. Мастера ищутся через psql.
- **Текстового поиска на сервере нет** — `--text` фильтрует title+description локально
  (task #44 «Быстрый поиск по задачам и эпикам» ещё в бэклоге).
- **Зависимости**: `depends_on`/`blocks` — массивы id; задача не переводится в `done`,
  пока зависимости не закрыты (REST вернёт 409). Блокеры — запрос в `references/queries.md`.
- Даты: `due_date` в JSON — RFC3339 (часто `T00:00:00Z`); сравнивай по дате, не по времени.
- 401 → обнови `AKHFLOW_TOKEN` в `.env`; API не отвечает → `./bin/akhflow serve api` (или `make up`).
- Данные меняются другими агентами/CLI: не кэшируй дольше одной команды, перечитывай перед решениями.

## 4. Как отвечать пользователю

- По-русски и кратко: сначала сводка (`21 открытая: backlog 8, todo 13`), затем 3–10 релевантных
  строк в виде `#id — title (status, priority, due, epic)`; просроченные помечай явно.
- Не вываливай сырой JSON, если не просили (`--json` — для программной обработки).
- Если нашёл кандидата для старта: назови его и переведи в работу (`./bin/akhflow task move <id>
  --status in_progress` или MCP `move_task`), затем комментируй прогресс.
- Если задач много и фильтр не сузился — уточни критерий (статус/эпик/приоритет), не листай всё.

## 5. Ресурсы

- `references/queries.md` — psql-рецепты: матрица status×priority, overdue, прогресс эпиков,
  блокеры, загрузка исполнителей, recent audit, recurring-мастера, зависшие in_progress.
- `scripts/backlog_search.py` — исполняемый хелпер из корня репозитория (единственный источник
  фильтрации; при изменении полей API обнови и его).
