package store

import (
	"context"
	"errors"

	"akhflow.ai/akhflow/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ImportBitrixTask atomically creates a task once per project, portal and external ID.
func (s *Store) ImportBitrixTask(ctx context.Context, portal, externalID string, task domain.Task) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	key := portal + ":" + externalID
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, $2))`, key, task.ProjectID)
	if err != nil {
		return false, err
	}
	var existing int64
	err = tx.QueryRow(ctx, `SELECT task_id FROM bitrix24_imports WHERE project_id=$1 AND portal=$2 AND external_id=$3`, task.ProjectID, portal, externalID).Scan(&existing)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	var taskID int64
	err = tx.QueryRow(ctx, `INSERT INTO tasks(project_id,title,description,status,priority,due_date,author_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, task.ProjectID, task.Title, task.Description, task.Status, task.Priority, task.DueDate, task.AuthorID).Scan(&taskID)
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO bitrix24_imports(project_id,portal,external_id,task_id) VALUES($1,$2,$3,$4)`, task.ProjectID, portal, externalID, taskID)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
