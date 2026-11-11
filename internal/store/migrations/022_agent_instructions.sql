ALTER TABLE projects ADD COLUMN agent_instructions TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN agent_instructions_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN agent_instructions_updated_at TIMESTAMPTZ;
ALTER TABLE agent_runs ADD COLUMN instructions_snapshot TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_runs ADD COLUMN instructions_version BIGINT NOT NULL DEFAULT 0;
