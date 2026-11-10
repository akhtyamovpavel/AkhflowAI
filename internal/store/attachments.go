package store

import (
	"context"
	"errors"

	"akhflow.ai/akhflow/internal/domain"
	"github.com/jackc/pgx/v5"
)

// CreateAttachment stores the metadata and bytes atomically in the task database.
func (s *Store) CreateAttachment(ctx context.Context, taskID int64, filename, key string, content []byte) (*domain.Attachment, error) {
	var attachment domain.Attachment
	query := `INSERT INTO attachments(task_id, filename, size_bytes, storage_key, content)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, task_id, filename, size_bytes, storage_key, created_at`
	err := s.db.QueryRow(ctx, query, taskID, filename, len(content), key, content).Scan(
		&attachment.ID, &attachment.TaskID, &attachment.Filename,
		&attachment.SizeBytes, &attachment.StorageKey, &attachment.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &attachment, nil
}

func (s *Store) ListAttachments(ctx context.Context, taskID int64) ([]domain.Attachment, error) {
	rows, err := s.db.Query(ctx, `SELECT id, task_id, filename, size_bytes, storage_key, created_at FROM attachments WHERE task_id=$1 AND content IS NOT NULL ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attachments := []domain.Attachment{}
	for rows.Next() {
		var attachment domain.Attachment
		err := rows.Scan(&attachment.ID, &attachment.TaskID, &attachment.Filename, &attachment.SizeBytes, &attachment.StorageKey, &attachment.CreatedAt)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, rows.Err()
}

func (s *Store) AttachmentContent(ctx context.Context, taskID, attachmentID int64) (*domain.Attachment, []byte, error) {
	var attachment domain.Attachment
	var content []byte
	query := `SELECT id, task_id, filename, size_bytes, storage_key, created_at, content
		FROM attachments WHERE id=$1 AND task_id=$2 AND content IS NOT NULL`
	err := s.db.QueryRow(ctx, query, attachmentID, taskID).Scan(
		&attachment.ID, &attachment.TaskID, &attachment.Filename,
		&attachment.SizeBytes, &attachment.StorageKey, &attachment.CreatedAt, &content,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &attachment, content, nil
}
