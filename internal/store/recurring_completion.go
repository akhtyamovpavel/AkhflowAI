package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"akhflow.ai/akhflow/internal/domain"
	"github.com/jackc/pgx/v5"
)

type TaskTransition struct {
	StatusChanged bool
	Next          *domain.Task
}

// UpdateTaskWithRecurrence commits the transition and its next occurrence together.
func (s *Store) UpdateTaskWithRecurrence(ctx context.Context, id int64, sets []string, args []any, actor string, actorID int64) (TaskTransition, error) {
	var result TaskTransition
	if len(sets) == 0 {
		return result, nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	var oldStatus string
	var spawned bool
	err = tx.QueryRow(ctx, `SELECT status, recurrence_spawned FROM tasks WHERE id=$1 FOR UPDATE`, id).Scan(&oldStatus, &spawned)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	clauses := make([]string, 0, len(sets))
	for i, column := range sets {
		clauses = append(clauses, fmt.Sprintf("%s=$%d", column, i+1))
	}
	params := append(append([]any{}, args...), id)
	query := fmt.Sprintf(`UPDATE tasks SET %s, updated_at=now() WHERE id=$%d RETURNING %s`, strings.Join(clauses, ", "), len(params), taskCols)
	task, err := s.scanTask(tx.QueryRow(ctx, query, params...))
	if err != nil {
		return result, err
	}
	result.StatusChanged = task.Status != oldStatus
	if result.StatusChanged && task.Status == domain.StatusDone && task.Recurring != "" && !spawned {
		var due any
		if task.DueDate != nil {
			if step, ok := domain.RecurrenceStep(task.Recurring); ok {
				due = step(*task.DueDate)
			}
		}
		var nextID int64
		err = tx.QueryRow(ctx, `INSERT INTO tasks(project_id, parent_id, title, description, acceptance_criteria, status, priority, estimate_min, due_date, author_id, assignee_id, recurring, epic_id)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id`,
			task.ProjectID, task.ParentID, task.Title, task.Description, task.AcceptanceCriteria, domain.StatusTodo, task.Priority, task.EstimateMin, due, task.AuthorID, task.AssigneeID, task.Recurring, task.EpicID).Scan(&nextID)
		if err != nil {
			return TaskTransition{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO task_labels(task_id, label) SELECT $2, label FROM task_labels WHERE task_id=$1`, id, nextID); err != nil {
			return TaskTransition{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks SET recurrence_spawned=TRUE WHERE id=$1`, id); err != nil {
			return TaskTransition{}, err
		}
		payload := map[string]any{"from": id}
		if _, err := tx.Exec(ctx, `INSERT INTO audit_log(actor, action, entity, entity_id, payload) VALUES($1,'recur_task','task',$2,$3)`, actor, nextID, payload); err != nil {
			return TaskTransition{}, err
		}
		if actorID != 0 {
			comment := fmt.Sprintf("повторение: создана задача #%d", nextID)
			if _, err := tx.Exec(ctx, `INSERT INTO comments(task_id, author_id, body) VALUES($1,$2,$3)`, id, actorID, comment); err != nil {
				return TaskTransition{}, err
			}
		}
		result.Next = &domain.Task{ID: nextID, ProjectID: task.ProjectID, Title: task.Title}
	}
	if err := tx.Commit(ctx); err != nil {
		return TaskTransition{}, err
	}
	return result, nil
}
