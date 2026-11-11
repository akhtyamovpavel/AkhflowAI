package store

import (
	"context"
	"errors"
	"time"

	"akhflow.ai/akhflow/internal/domain"
	"github.com/jackc/pgx/v5"
)

// CreateScheduledOccurrence advances an eligible master and creates one occurrence.
// A nil task means another scheduler or a user already changed the master.
func (s *Store) CreateScheduledOccurrence(ctx context.Context, masterID int64, now time.Time) (*domain.Task, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	query := `SELECT ` + taskCols + ` FROM tasks
		WHERE id=$1 AND recurring != '' AND due_date IS NOT NULL AND due_date <= $2::date AND status != $3 FOR UPDATE`
	master, err := s.scanTask(tx.QueryRow(ctx, query, masterID, now.Format(time.DateOnly), domain.StatusDone))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	step, valid := domain.RecurrenceStep(master.Recurring)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	todayDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if !valid {
		return nil, nil
	}
	nextDue := step(*master.DueDate)
	for !nextDue.After(todayDate) {
		nextDue = step(nextDue)
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO tasks
		(project_id, parent_id, title, description, acceptance_criteria, status, priority, estimate_min, due_date, author_id, assignee_id, recurring, epic_id)
		SELECT project_id, parent_id, title, description, acceptance_criteria, $2, priority, estimate_min, $3::date, author_id, assignee_id, '', epic_id
		FROM tasks WHERE id=$1 RETURNING id`, masterID, domain.StatusTodo, today.Format(time.DateOnly)).Scan(&id)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO task_labels (task_id, label) SELECT $1, label FROM task_labels WHERE task_id=$2`, id, masterID)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE tasks SET due_date=$2::date, updated_at=now() WHERE id=$1`, masterID, nextDue.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	master.ID = id
	master.Status = domain.StatusTodo
	master.Recurring = ""
	master.DueDate = &today
	return master, nil
}
