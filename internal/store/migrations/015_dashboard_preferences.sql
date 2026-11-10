CREATE TABLE dashboard_preferences (
    project_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    hidden_widgets TEXT[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (project_id, user_id),
    FOREIGN KEY (project_id, user_id) REFERENCES project_members(project_id, user_id) ON DELETE CASCADE
);
