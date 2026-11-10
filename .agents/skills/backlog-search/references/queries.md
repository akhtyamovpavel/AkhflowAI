# SQL-рецепты для бэклога Akhflow (read-only)

Прямой доступ к Postgres полезен для агрегатов, `recurring`-мастеров и зависимостей: списки
REST/CLI/MCP этого не отдают. Все запросы ниже — **только на чтение**. Для изменений используй
`./bin/akhflow`, REST или MCP: прямая запись в БД не порождает событий SSE и записей в `audit_log`.

Подключение к контейнеру из `docker-compose.yml`:

```bash
docker exec -i akhflow-ai-db-1 psql -U akhflow -d akhflow
# компактный вывод для вставки в таблицу:
docker exec -i akhflow-ai-db-1 psql -U akhflow -d akhflow -t -A -F'|' -c "<SQL>"
```

Подстановка проекта по slug: `(SELECT id FROM projects WHERE slug = 'akhflow')`.
Везде ниже `recurring = ''` — реальные задачи; `recurring != ''` — мастера повторений.

## Матрица status × priority

```sql
SELECT status, priority, count(*)
FROM tasks
WHERE project_id = (SELECT id FROM projects WHERE slug = 'akhflow')
  AND recurring = ''
GROUP BY status, priority
ORDER BY CASE status WHEN 'backlog' THEN 0 WHEN 'todo' THEN 1 WHEN 'in_progress' THEN 2 ELSE 3 END,
         CASE priority WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END;
```

## Открытые задачи с эпиком, исполнителем и дедлайном

```sql
SELECT t.id, t.status, t.priority,
       coalesce(e.title, '—') AS epic,
       coalesce(u.display_name, '—') AS assignee,
       t.due_date, t.title
FROM tasks t
LEFT JOIN epics e ON e.id = t.epic_id
LEFT JOIN users u ON u.id = t.assignee_id
WHERE t.project_id = (SELECT id FROM projects WHERE slug = 'akhflow')
  AND t.recurring = ''
  AND t.status <> 'done'
ORDER BY CASE t.priority WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END, t.id;
```

## Просроченные

```sql
SELECT id, status, priority, due_date, title
FROM tasks
WHERE project_id = (SELECT id FROM projects WHERE slug = 'akhflow')
  AND recurring = '' AND status <> 'done'
  AND due_date IS NOT NULL AND due_date < current_date
ORDER BY due_date;
```

## Прогресс эпиков (done / total)

```sql
SELECT e.id, e.title,
       count(t.id) AS total,
       count(t.id) FILTER (WHERE t.status = 'done') AS done
FROM epics e
LEFT JOIN tasks t ON t.epic_id = e.id AND t.recurring = ''
WHERE e.project_id = (SELECT id FROM projects WHERE slug = 'akhflow')
GROUP BY e.id, e.title
ORDER BY e.id;
```

## Метки (распределение по открытым)

```sql
SELECT tl.label, count(*) AS open
FROM task_labels tl
JOIN tasks t ON t.id = tl.task_id
WHERE t.project_id = (SELECT id FROM projects WHERE slug = 'akhflow')
  AND t.status <> 'done' AND t.recurring = ''
GROUP BY tl.label
ORDER BY open DESC;
```

## Блокеры: открытые задачи, ждущие незавершённые зависимости

```sql
SELECT t.id AS blocked_id, t.title AS blocked_title,
       d.depends_on AS dependency_id, dt.status AS dependency_status, dt.title AS dependency_title
FROM task_dependencies d
JOIN tasks t ON t.id = d.task_id
JOIN tasks dt ON dt.id = d.depends_on
WHERE t.project_id = (SELECT id FROM projects WHERE slug = 'akhflow')
  AND t.status <> 'done'
  AND dt.status <> 'done'
ORDER BY t.id;
```

## Recurring-мастера (скрыты из REST/CLI/MCP-списков)

```sql
SELECT id, title, recurring, due_date, status
FROM tasks
WHERE project_id = (SELECT id FROM projects WHERE slug = 'akhflow')
  AND recurring <> ''
ORDER BY due_date;
```

## Зависшие in_progress

```sql
SELECT id, title, updated_at::date AS updated, current_date - updated_at::date AS days
FROM tasks
WHERE project_id = (SELECT id FROM projects WHERE slug = 'akhflow')
  AND status = 'in_progress'
ORDER BY updated_at;
```

## Загрузка исполнителей (открытые задачи)

```sql
SELECT coalesce(u.display_name, '— unassigned —') AS assignee, count(*) AS open
FROM tasks t
LEFT JOIN users u ON u.id = t.assignee_id
WHERE t.project_id = (SELECT id FROM projects WHERE slug = 'akhflow')
  AND t.recurring = '' AND t.status <> 'done'
GROUP BY 1
ORDER BY open DESC;
```

## Последние события audit_log по задачам проекта

```sql
SELECT a.id, a.actor, a.action, a.entity_id, a.payload, a.created_at
FROM audit_log a
JOIN tasks t ON t.id = a.entity_id
WHERE a.entity = 'task'
  AND t.project_id = (SELECT id FROM projects WHERE slug = 'akhflow')
ORDER BY a.id DESC
LIMIT 20;
```

## Полнотекстовый поиск по заголовку/описанию (когда REST-фильтров не хватает)

```sql
SELECT id, status, priority, due_date, title
FROM tasks
WHERE project_id = (SELECT id FROM projects WHERE slug = 'akhflow')
  AND recurring = ''
  AND (title ILIKE '%импорт%' OR description ILIKE '%импорт%')
ORDER BY id DESC;
```
