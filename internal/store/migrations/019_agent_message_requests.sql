CREATE TABLE agent_message_requests (
    chat_id BIGINT NOT NULL REFERENCES agent_chats(id) ON DELETE CASCADE,
    actor_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    request_key UUID NOT NULL,
    message TEXT NOT NULL,
    result JSONB,
    PRIMARY KEY (chat_id, actor_id, request_key)
);
