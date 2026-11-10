CREATE TABLE IF NOT EXISTS trello_import_lists (
    project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    board_id TEXT NOT NULL,
    list_id TEXT NOT NULL,
    epic_id BIGINT NOT NULL REFERENCES epics(id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, board_id, list_id)
);

CREATE TABLE IF NOT EXISTS trello_import_cards (
    project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    board_id TEXT NOT NULL,
    card_id TEXT NOT NULL,
    task_id BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, board_id, card_id)
);
