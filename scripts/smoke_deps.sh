#!/usr/bin/env bash
# Smoke test for task dependencies (backlog #23).
# Creates a throwaway project, exercises the dependency API, prints PASS/FAIL per check.
set -uo pipefail
cd "$(dirname "$0")/.."
mkdir -p e2e/artifacts

TOKEN=$(grep -o 'AKHFLOW=.*' /dev/null 2>/dev/null; grep -o 'AKHFLOW_TOKEN=.*' .env | cut -d= -f2)
API=${AKHFLOW_API:-http://localhost:8080}
PASS=0; FAIL=0

check() { # name expected_code actual_code
  if [ "$2" = "$3" ]; then echo "PASS: $1 ($3)"; PASS=$((PASS+1));
  else echo "FAIL: $1 (want $2, got $3)"; FAIL=$((FAIL+1)); fi
}

code() { curl -s -o e2e/artifacts/deps_body.json -w '%{http_code}' "$@"; }

PROJ=$(curl -s -X POST "$API/api/projects" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d "{\"name\":\"Smoke Deps $(date +%s)\"}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["slug"])')
echo "project: $PROJ"

mk() { curl -s -X POST "$API/api/projects/$PROJ/tasks" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d "{\"title\":\"$1\"}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])'; }

A=$(mk "A: основа")
B=$(mk "B: надстройка")
echo "tasks: A=$A B=$B"

# B depends on A -> 201
C=$(code -X POST "$API/api/tasks/$B/dependencies" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d "{\"depends_on\":$A}")
check "add dependency B->A" 201 "$C"

# GET /api/tasks/B: dependencies contain A, blocks of A contain B
curl -s "$API/api/tasks/$B" -H "Authorization: Bearer $TOKEN" > e2e/artifacts/deps_body.json
python3 -c "
import json,sys
d = json.load(open('e2e/artifacts/deps_body.json'))
deps = [t['id'] for t in d['dependencies']]
blocks = [t['id'] for t in d['blocks']]
assert $A in deps, deps
print('PASS: GET task B lists dependency A')
" && PASS=$((PASS+1)) || FAIL=$((FAIL+1))
curl -s "$API/api/tasks/$A" -H "Authorization: Bearer $TOKEN" > e2e/artifacts/deps_body.json
python3 -c "
import json
d = json.load(open('e2e/artifacts/deps_body.json'))
assert $B in [t['id'] for t in d['blocks']], d['blocks']
print('PASS: GET task A lists dependent B')
" && PASS=$((PASS+1)) || FAIL=$((FAIL+1))

# cycle: A depends on B -> 409
C=$(code -X POST "$API/api/tasks/$A/dependencies" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d "{\"depends_on\":$B}")
check "cycle rejected" 409 "$C"

# self dependency -> 400
C=$(code -X POST "$API/api/tasks/$A/dependencies" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d "{\"depends_on\":$A}")
check "self dependency rejected" 400 "$C"

# B -> done while A unfinished -> 409
C=$(code -X PATCH "$API/api/tasks/$B" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"status":"done"}')
check "done blocked by unfinished dep" 409 "$C"

# A -> done, then B -> ok
C=$(code -X PATCH "$API/api/tasks/$A" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"status":"done"}')
check "A done" 200 "$C"
C=$(code -X PATCH "$API/api/tasks/$B" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"status":"done"}')
check "B done after dep done" 200 "$C"

# remove dependency -> 200
C=$(code -X DELETE "$API/api/tasks/$B/dependencies/$A" -H "Authorization: Bearer $TOKEN")
check "remove dependency" 200 "$C"

# list endpoint
C=$(code "$API/api/tasks/$B/dependencies" -H "Authorization: Bearer $TOKEN")
check "GET dependencies list" 200 "$C"

echo "---"
echo "RESULT: $PASS passed, $FAIL failed"
[ "$FAIL" = "0" ]
