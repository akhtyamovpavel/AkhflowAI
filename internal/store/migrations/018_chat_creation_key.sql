ALTER TABLE agent_chats ADD COLUMN creation_key UUID;

CREATE UNIQUE INDEX agent_chats_creation_key ON agent_chats(project_id, user_id, creation_key);
