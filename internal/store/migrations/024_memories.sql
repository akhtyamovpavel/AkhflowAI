CREATE TABLE memories (
    id BIGSERIAL PRIMARY KEY,
    project_id BIGINT REFERENCES projects(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope TEXT NOT NULL CHECK (scope IN ('project', 'user')),
    content TEXT NOT NULL CHECK (octet_length(content) <= 16384),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX memories_project_idx ON memories(project_id);
CREATE INDEX memories_user_idx ON memories(user_id);
