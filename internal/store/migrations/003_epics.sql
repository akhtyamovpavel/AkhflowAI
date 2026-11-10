-- 003_epics

CREATE TABLE IF NOT EXISTS epics (
    id BIGSERIAL PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'backlog',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_epics_project ON epics(project_id, id DESC);

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS epic_id BIGINT REFERENCES epics(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_epic ON tasks(epic_id);
