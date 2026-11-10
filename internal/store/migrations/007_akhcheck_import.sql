CREATE TABLE IF NOT EXISTS akhcheck_imports (
    project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    course_id BIGINT NOT NULL,
    assignment_id BIGINT NOT NULL,
    task_id BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, course_id, assignment_id)
);
