CREATE TABLE agent_runs (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL REFERENCES agent_chats(id) ON DELETE CASCADE,
    actor_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    request_key UUID NOT NULL,
    message TEXT NOT NULL,
    history JSONB NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'step_limit', 'failed', 'canceled', 'interrupted')),
    iteration INTEGER NOT NULL DEFAULT 0,
    tool TEXT,
    result JSONB,
    error_code TEXT,
    cancel_requested BOOLEAN NOT NULL DEFAULT false,
    lease_token UUID,
    lease_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    UNIQUE (chat_id, actor_id, request_key)
);
CREATE UNIQUE INDEX agent_runs_one_active_chat ON agent_runs(chat_id) WHERE status IN ('queued', 'running');
CREATE INDEX agent_runs_queue ON agent_runs(id) WHERE status = 'queued';
CREATE TABLE agent_run_steps (
    run_id BIGINT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    iteration INTEGER NOT NULL,
    tool TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'interrupted')),
    summary TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    PRIMARY KEY (run_id, sequence)
);
