-- Notification payloads contain only opaque identifiers, never task details.
CREATE TABLE IF NOT EXISTS mcp_events (
    id TEXT PRIMARY KEY,
    event JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mcp_events_created_at_idx ON mcp_events (created_at);
