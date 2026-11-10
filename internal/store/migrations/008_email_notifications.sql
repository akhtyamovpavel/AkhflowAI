ALTER TABLE users ADD COLUMN IF NOT EXISTS email_notifications BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS email_notifications (
    id BIGSERIAL PRIMARY KEY,
    task_id BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    recipient_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    task_title TEXT NOT NULL,
    old_status TEXT NOT NULL,
    new_status TEXT NOT NULL,
    attempts INT NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS email_notifications_pending ON email_notifications (available_at, id);

CREATE OR REPLACE FUNCTION enqueue_task_status_email() RETURNS trigger AS $$
BEGIN
    IF OLD.status IS DISTINCT FROM NEW.status AND NEW.assignee_id IS NOT NULL THEN
        INSERT INTO email_notifications(task_id, project_id, recipient_id, task_title, old_status, new_status)
        SELECT NEW.id, NEW.project_id, u.id, NEW.title, OLD.status, NEW.status
        FROM users u
        JOIN project_members pm ON pm.user_id = u.id AND pm.project_id = NEW.project_id
        WHERE u.id = NEW.assignee_id AND u.email_notifications;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS task_status_email ON tasks;
CREATE TRIGGER task_status_email AFTER UPDATE OF status ON tasks
FOR EACH ROW EXECUTE FUNCTION enqueue_task_status_email();
