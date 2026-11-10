#!/usr/bin/env bash
# Seed the Akhflow project backlog. Idempotent: skips existing titles.
# Usage: ./scripts/seed_backlog.sh [project-slug]
set -euo pipefail
cd "$(dirname "$0")/.."

TOKEN=$(grep -o 'AKHFLOW_TOKEN=.*' .env | cut -d= -f2)
API=${AKHFLOW_API:-http://localhost:8080}
PROJECT=${1:-${PROJECT:-akhflow}}

existing=$(curl -s "$API/api/projects/$PROJECT/tasks" -H "Authorization: Bearer $TOKEN" \
  | python3 -c 'import json,sys
try:
    print("\n".join(t["title"] for t in json.load(sys.stdin)))
except Exception:
    pass')

create() {
  local title="$1" prio="$2" labels="$3"
  if [ -n "$existing" ] && grep -qxF "$title" <<< "$existing"; then
    echo "skip (exists): $title"
    return
  fi
  local body
  body=$(python3 -c 'import json,sys; print(json.dumps({"title":sys.argv[1],"status":"backlog","priority":sys.argv[2],"labels":sys.argv[3].split(",")}))' "$title" "$prio" "$labels")
  local code
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/api/projects/$PROJECT/tasks" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$body")
  if [ "$code" = "201" ]; then
    echo "+ $title"
  else
    echo "skip ($code): $title"
  fi
}

create "Импорт задач из Trello (API)" high import
create "Импорт задач из Bitrix24 (API)" high import
create "Импорт задач из Akhcheck (export/API)" high import
create "MCP over HTTP: авторизация по MCP_API_KEY" high mcp
create "Контейнеризация API: Dockerfile и compose для всего стека" high devops
create "Зависимости между задачами: API + UI" medium feature
create "Повторяющиеся задачи: планировщик recurring" medium feature
create "Оценка времени и дедлайны в UI канбана" medium ui
create "Email-уведомления о смене статуса" medium notifications
create "Браузерные уведомления (SSE)" medium notifications
create "CI: сборка, vet и тесты в GitHub Actions" medium devops
create "Роли editor/viewer в проекте" low auth
create "Загрузка вложений к задачам" low feature
create "Метрики использования агента" low metrics
create "Создание задач естественным языком" low ai

echo "--- BACKLOG ($PROJECT) ---"
./bin/akhflow task list -p "$PROJECT"
