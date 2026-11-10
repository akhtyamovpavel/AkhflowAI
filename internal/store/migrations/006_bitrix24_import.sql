CREATE TABLE IF NOT EXISTS bitrix24_imports (
    project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    portal TEXT NOT NULL,
    external_id TEXT NOT NULL,
    task_id BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, portal, external_id)
);
