package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type EmailNotification struct {
	ID        int64
	TaskID    int64
	Recipient string
	Title     string
	OldStatus string
	NewStatus string
}

func (s *Store) EmailNotificationsEnabled(ctx context.Context, userID int64) (bool, error) {
	var enabled bool
	err := s.db.QueryRow(ctx, `SELECT email_notifications FROM users WHERE id=$1`, userID).Scan(&enabled)
	return enabled, err
}

func (s *Store) SetEmailNotifications(ctx context.Context, userID int64, enabled bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE users SET email_notifications=$1 WHERE id=$2`, enabled, userID); err != nil {
		return err
	}
	if !enabled {
		if _, err := tx.Exec(ctx, `DELETE FROM email_notifications WHERE recipient_id=$1`, userID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ClaimEmailNotification leases a pending notification so multiple workers do not send it concurrently.
func (s *Store) ClaimEmailNotification(ctx context.Context) (*EmailNotification, error) {
	if _, err := s.db.Exec(ctx, `DELETE FROM email_notifications n WHERE NOT EXISTS (
		SELECT 1 FROM users u JOIN project_members pm ON pm.user_id=u.id
		WHERE u.id=n.recipient_id AND u.email_notifications AND pm.project_id=n.project_id
	)`); err != nil {
		return nil, err
	}
	var item EmailNotification
	err := s.db.QueryRow(ctx, `WITH candidate AS (
		SELECT id FROM email_notifications WHERE available_at <= now()
		ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1
	), claimed AS (
		UPDATE email_notifications n SET available_at=now()+interval '2 minutes', attempts=attempts+1
		FROM candidate c WHERE n.id=c.id
		RETURNING n.id,n.task_id,n.recipient_id,n.task_title,n.old_status,n.new_status
	)
	SELECT c.id,c.task_id,u.email,c.task_title,c.old_status,c.new_status
	FROM claimed c JOIN users u ON u.id=c.recipient_id`).Scan(
		&item.ID, &item.TaskID, &item.Recipient, &item.Title, &item.OldStatus, &item.NewStatus,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &item, err
}

func (s *Store) DeleteEmailNotification(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM email_notifications WHERE id=$1`, id)
	return err
}
