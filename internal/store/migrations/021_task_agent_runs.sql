ALTER TABLE agent_runs ADD COLUMN task_id BIGINT REFERENCES tasks(id) ON DELETE SET NULL;
ALTER TABLE agent_runs ADD COLUMN task_snapshot JSONB;
CREATE UNIQUE INDEX agent_runs_task_request ON agent_runs(task_id, actor_id, request_key) WHERE task_id IS NOT NULL;
CREATE INDEX agent_runs_task_list ON agent_runs(task_id, id DESC) WHERE task_id IS NOT NULL;
