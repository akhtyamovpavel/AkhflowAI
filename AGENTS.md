# AGENTS.md — Akhflow-AI

AI-native task tracker. Go monorepo + React web. Russian-language UI.

## Commands

```bash
# build / verify (order matters)
go build -o bin/akhflow ./cmd/akhflow && go vet ./...
cd web && npm install && npm run build        # tsc -b && vite build

# infra + start (this order)
make up                                       # docker compose up -d --build (db + api + web, api migrates itself)
make up-db                                    # только БД для локального запуска API
./bin/akhflow migrate                         # embedded SQL migrations (если БД поднята отдельно)
setsid nohup ./bin/akhflow serve api > api.log 2>&1 < /dev/null & disown   # plain nohup DIES when bash session ends
ssh -f -N -L 11434:127.0.0.1:11434 gpu-server   # tunnel to local LLM (required for agent)
cd web && npm run dev                         # :5173, proxies /api -> :8080

# CLI (reads .env; AKHFLOW_API / AKHFLOW_TOKEN env vars)
./bin/akhflow task list -p <slug> [--status]
./bin/akhflow task create -p <slug> --title ... [--priority low|medium|high] [--due YYYY-MM-DD]
./bin/akhflow task move <id> --status <status>
./bin/akhflow task delete <id>            # 409 while other tasks depend on it
./bin/akhflow agent "<message>"               # end-to-end agent test
./bin/akhflow serve mcp                       # MCP stdio; add --http :9090 for /mcp endpoint

pkill -x akhflow / pgrep -x akhflow           # use -x! pgrep -f matches the invoking shell's own cmdline

# verify MCP tool registry over stdio (initialize + tools/list)
printf '%s\n%s\n' \
'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
'{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | ./bin/akhflow serve mcp
```

## Publish checks

- Enable the local pre-commit hook after cloning: `git config core.hooksPath .githooks`.
- The hook requires Python 3 and Gitleaks on PATH or at `bin/security-tools/gitleaks`. It scans the complete index, blocks local secret values, sensitive filenames and personal absolute paths, and redacts scanner output. Missing Gitleaks blocks the commit.
- Check publishable worktree files with `python3 scripts/check_publish.py`; check the index with `python3 scripts/check_publish.py --staged`. Run hook regression checks with `python3 scripts/test_publish_hook.py`.
- Generate separate random values for `JWT_SECRET` and `MCP_API_KEY` in the ignored `.env`; Compose requires `JWT_SECRET`. Optional UI text linting in `scripts/smoke_deadlines.mjs` uses `TEXT_LINT_SCRIPT` and `PYTHON_BIN`.
- Trello import uses `TRELLO_API_KEY`, `TRELLO_API_TOKEN`, and `TRELLO_IMPORT_USER_ID` from server environment. Set the user ID to the account authorized to use those credentials; other users cannot import with them. Open Trello lists become epics, open cards become backlog tasks, and re-import skips existing cards without overwriting local edits. Credentials are never sent through the agent or stored in task history.

## Git workflow

- Each issue gets its own Git worktree and branch. Select tracker issues from the primary worktree, then implement and verify each issue in its dedicated worktree under `.worktrees/`.
- Pull before push: when the issue branch has an upstream, fetch and integrate its latest changes before pushing. Resolve conflicts, rerun the relevant checks and preserve the intent of both sides. For a new branch without an upstream, check the remote before the first push.
- Agents may create commits and push automatically on branches other than `main` after the relevant checks and the pre-commit hook pass.
- Commits and pushes to `main` require an explicit user instruction. Permission for one operation on `main` does not authorize later automatic commits or pushes to that branch.
- Before publishing, check the staged files and outgoing history for secrets and personal absolute paths. Keep local credentials, logs, build outputs and test artifacts out of Git.
- Before pushing, fetch and integrate the current upstream branch; for a new branch, first check whether it already exists on the remote. Resolve conflicts and rerun relevant checks after integration.

## Architecture

- Single Go module `akhflow.ai/akhflow` at root; one binary `cmd/akhflow` (cobra): `serve api|mcp`, `migrate`, `task`, `agent`
- `internal/toolbox` — single registry of tools (list/create/move/get/delete task, add_comment, set_assignee, list/create epic, set_task_epic, add/remove_dependency, set_task_recurring). ONE source of truth: MCP server and the LLM agent execute the same handlers. Add a feature here, not in server/mcp separately
- `internal/llm` — agent loop (max 8 iterations) over OpenAI-compatible `/v1/chat/completions`; system prompt forbids raw JSON dumps and requires a concise Russian summary of actions; agent is stateful WITHIN a chat: `sendMessage` loads last 20 turns from `agent_messages` into the LLM context (`/api/agent/chat` stays stateless); `internal/mcp` — hand-rolled JSON-RPC 2.0 (protocol 2025-06-18), stdio + HTTP
- `internal/server` — REST: argon2id + JWT (HS256), per-user project isolation via `project_members`
- `internal/store` — pgx/v5 + pgxpool, migrations embedded via `go:embed` in `internal/store/migrations/`
- `web/` — Vite + React 19 + TS + Tailwind v4 + shadCN (components in `src/components/ui/`); tool calls in agent chat render brief (`message` field / item count), not raw JSON — same in CLI `agent` output
- Statuses: backlog/todo/in_progress/done; priorities: low/medium/high
- `list_tasks` tool accepts `brief:true` → only `{id, title, status, priority}`, saves tokens in the agent loop; the LLM uses it by default for listings
- Epics: `epics` table per project, `tasks.epic_id` (migration 003); REST `/api/projects/{slug}/epics`, `/api/epics/{id}` (GET/DELETE); web: filter + create in header, epic column in list view, badge on kanban cards
- Dependencies: `task_dependencies` table (migration 001); REST `GET/POST /api/tasks/{id}/dependencies`, `DELETE /api/tasks/{id}/dependencies/{dep}`; tools `add_dependency`/`remove_dependency` (cycle check via recursive CTE, same-project check); moving to `done` is blocked (409) while any dependency is unfinished; web: «Зависит от»/«Блокирует» sections in task drawer, link badge + «ждёт» counter on kanban cards, column in list view
- Recurring tasks: `recurring` field (daily/weekly/monthly) on tasks; completion hook spawns next occurrence with advanced due_date; scheduler goroutine catches overdue open masters, skips missed intervals and spawns ONE occurrence for today, advances master to the next future due_date; masters (recurring != '') are hidden from task lists (ListTasks/ListTasksByEpic filter recurring=''); tool `set_task_recurring`; web: recurrence select in task drawer, Repeat badge on kanban
- Notifications (SSE): `GET /api/projects/{slug}/events` accepts a Bearer header or `?token=`; the header takes precedence. The bundled nginx disables access and error logging for this route to avoid recording query credentials; configure other reverse proxies to do the same. Events include `task_created|task_moved|task_updated|task_comment|dependency_added|dependency_removed`.
- Email status notifications: configure `SMTP_ADDR` (host:port) and `SMTP_FROM`, optionally `SMTP_USERNAME` and `SMTP_PASSWORD`. Remote SMTP requires STARTTLS; unauthenticated loopback SMTP is supported for tests. Each user opts in via the header or `GET/PUT /api/settings/email-notifications`; notifications go to the assigned project member on actual status changes through REST or MCP. A database outbox retries failed delivery; a crash after SMTP acceptance can cause a duplicate. No SMTP configuration means no sender and users cannot opt in.
- Akhcheck import: upload a saved JSON response of `GET /api/teaching/course/{id}` to `POST /api/projects/{slug}/imports/akhcheck` or use `import_akhcheck_course`. Assignment IDs are scoped by project and course; repeat imports skip existing tasks, with no external credentials stored.
- Task detail: drawer in web (click task title in kanban/list) — title, description, status/priority/epic/assignee/due_date/estimate_min, recurrence, dependencies, comments with author names, «История» (audit_log, человекочитаемые действия, `store.AuditForTask`); `GET /api/tasks/{id}` returns `{task, comments, dependencies, blocks, history}` (history = last 50 audit entries); tool `get_task` also returns `history`; `GET /api/projects/{slug}/members` gives project members (author/assignee names)
- Messenger sharing: «Обсудить в Telegram» opens a Telegram share draft with the task title and a link; the user chooses whether to send it. The link retains the deployment path and contains only the project slug and task ID. After login, the UI opens the task only if it is present in the selected accessible project's task list. This does not synchronize conversations or send messages from the server.
- Task attachments: project members can download files up to 10 MiB per file, and members with write access can upload them from the task drawer. `POST/GET /api/tasks/{id}/attachments` upload or list, `GET /api/tasks/{id}/attachments/{attachment}` downloads, and `list_attachments` lists metadata through MCP. Files are stored with their metadata in PostgreSQL and removed by the task's cascade; metadata from earlier versions without stored bytes is preserved but cannot be downloaded.
- Project roles: `viewer` can read project data; `editor` can also change it. Existing `member` retains write access, and `owner`/`admin` can manage participants via `PUT /api/projects/{slug}/members` with a registered user's email and role (`editor` or `viewer`). The project owner's role cannot be changed. REST and the shared toolbox enforce roles; new project routes and tools must apply the same access checks.
- Project dashboard: «Сводка» aggregates all listed project tasks by status, priority and assignee using existing task/member endpoints; it refreshes on local task updates and on request. The epic filter does not affect dashboard totals. Tests: `node scripts/test_dashboard.mjs` and `PLAYWRIGHT_MODULE=<playwright entry> node scripts/test_dashboard_ui.mjs`.
- Agent usage: `GET /api/projects/{slug}/agent/usage?days=7|30|0` and the `get_agent_usage` tool count saved project chat requests, replies and tool calls for rolling 7 or 30 days, or all time (`0`). The «Агент» view displays these counts. Stateless CLI requests and agent replies that were not saved are not included; no extra conversation content is stored.
- Bitrix24 import: configure `BITRIX24_WEBHOOK_URL` in the API environment with an HTTPS incoming webhook that has task access, and set `BITRIX24_ALLOWED_USER_ID` to the Akhflow user allowed to use that credential. `POST /api/projects/{slug}/imports/bitrix24` and `import_bitrix24_tasks` accept an optional numeric `group_id`; the project UI offers the same import. Tasks visible to the webhook are imported once per project, portal and task ID; repeats skip existing tasks and do not overwrite local changes. Descriptions, statuses, priorities and due dates are copied; assignees and attachments are not imported.
- Feature recipe (order matters): migrations → `internal/store` → `internal/toolbox` (registry) → `internal/server` routes → `web/src/App.tsx`; then rebuild binary, `pkill -x akhflow` (kills BOTH api and mcp), restart both via `setsid nohup`, verify three ways: REST smoke script, MCP `tools/list` over stdio, agent e2e via CLI
- Containers: root `Dockerfile` (API, golang:1.26-alpine → alpine, non-root), `web/Dockerfile` + `web/nginx.conf` (node:22 build → nginx, `/api/` proxied to `api:8080`); compose stacks db+api+web, api migrates on start (`command: sh -c "akhflow migrate && exec akhflow serve api"`); OLLAMA_HOST defaults to `http://host.docker.internal:11434` via `host-gateway` (tunnel must be up on the host); CI: `.github/workflows/ci.yml` (go build/vet/test + npm ci/build)

## DSH agents (local integration)

- `scripts/dsh-akhflow-solve.patch.yml` — preset `akhflow-solve`: трекер Akhflow через MCP (`akhflow serve mcp` stdio) + полный кодинг-тулсет; агент сам берёт задачи, проверяет сборки и закрывает их.
- `scripts/dsh-akhflow-e2e.patch.yml` — preset `akhflow-e2e`: e2e-тестирование UI через Playwright MCP (`npx -y @playwright/mcp@0.0.83`, headless system Chrome, isolated, `--output-dir e2e/artifacts`) + трекер по MCP для сверки. Скриншоты: `browser_take_screenshot` **без `filename`** → image-контент в выходе тула (видно в диалоге); сценарии — `e2e/README.md`.
- Оба фрагмента дописываем в `~/.dsh/profiles/web/cordis.patch.yml` с бэкапом; HMR подхватывает изменения без рестарта. Перед запуском DSH задаём `AKHFLOW_ROOT` с каталогом репозитория или запускаем DSH из корня проекта. MCP читает настройки подключения из локального `.env`.

## Gotchas (all bit us already)

- **Postgres 18 image**: volume must mount at `/var/lib/postgresql` (NOT `.../data`) or container exits silently with "pg_ctlcluster" error
- `pg_isready` inside container passes before host port accepts connections — retry `migrate` a few seconds after `compose up`
- **Go nil slices marshal to JSON `null`** — always init `out := []T{}` in list queries, or React `.map` crashes
- `UpdateTask` SQL: `$N` numbering counts only real params (`updated_at=now()` takes none)
- Tailwind v4 + shadCN: colors need `@theme inline { --color-x: var(--x) }` mapping in `index.css`, else "Cannot apply unknown utility class"
- JWT is stateless: auth middleware MUST check user existence in DB (done), else stale tokens from a recreated DB become ghost-users (FK 23503 on project create); web auto-logs-out on 401
- `.env` is loaded by the CLI itself (only vars not already in env); API/agent need `OLLAMA_HOST` + `AGENT_MODEL`; `.token`/`.env-cli` hold real secrets — never commit
- **Receiver shadowing in server.go**: handlers are methods on `*Server` (receiver `s`); a local string variable named `s` (e.g. `s, _ := v.(string)` in patchTask) silently shadows it — compile error `s.Store undefined`. Use `st` for status strings
- **Check migrations before building a feature**: `task_dependencies` shipped in 001_init.sql from day one but had zero code on top of it; grep `internal/store/migrations/` first, the schema may already exist
- **DELETE for tasks exists** (REST `DELETE /api/tasks/{id}`, CLI `task delete`, tool `delete_task`): cascades comments/labels/attachments/dependency links, returns 409 while other tasks depend on the target. No DELETE for projects — smoke tests must create a throwaway project (see `scripts/smoke_deps.sh`, it generates a unique slug per run), never pollute the real `akhflow` project
- Agent CLI posts to `/api/agent/chat` with an EMPTY project slug — the LLM infers the project from the message text, so name it explicitly in test prompts («В проекте smoke-deps …»)

## Model stack

- LLM runs on `gpu-server` (ssh host, RTX 3090): Ollama 0.35.1, model `qwen3.5:9b` (chosen for tool-calling, KAMI 88.1%)
- Ollama API bound to 127.0.0.1:11434 on gpu-server — always via SSH tunnel; no auth, keep it that way
- Model tool-calling tests are maintained separately on the configured LLM host.

## Conventions

- Backlog lives in the tracker itself (project `akhflow`, owner: akhtyamovpavel@gmail.com): `./bin/akhflow task list -p akhflow`; re-seed with `./scripts/seed_backlog.sh`. Take new work from there, move statuses via `task move` or the agent
- **Поиск по бэклогу**: DSH-skill `.agents/skills/backlog-search/` (проектный root — DSH-агенты в этом репо подхватывают автоматически, в т.ч. `akhflow-solve`) + хелпер `./scripts/backlog_search.py` (одна выборка REST, фильтры локально: `--status/--priority/--epic/--assignee/--label/--text/--overdue/--json`), SQL-рецепты — в `references/queries.md`. Не делать N+1 `get_task` по всем задачам списка
- Scripts live in `scripts/` (never /tmp) — see global CLAUDE.md rule
- No Go tests yet (`go test ./...` is empty); smoke test = register → create project → `akhflow agent "перемести задачу N в done"`; dependencies smoke test = `./scripts/smoke_deps.sh`
