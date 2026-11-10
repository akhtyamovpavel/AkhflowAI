package store

import (
	"context"
	"fmt"

	"akhflow.ai/akhflow/internal/akhcheck"
	"github.com/jackc/pgx/v5"
)

type AkhcheckImportResult struct {
	Created int `json:"created"`
	Skipped int `json:"skipped"`
}

// ImportAkhcheck atomically imports one course and serializes concurrent imports.
func (s *Store) ImportAkhcheck(ctx context.Context, projectID, actorID int64, course akhcheck.Course) (AkhcheckImportResult, error) {
	result := AkhcheckImportResult{}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	lock := fmt.Sprintf("akhcheck:%d:%d", projectID, course.ID)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lock); err != nil {
		return result, err
	}
	for _, assignment := range course.Tasks {
		var existing int64
		err := tx.QueryRow(ctx, `SELECT task_id FROM akhcheck_imports WHERE project_id=$1 AND course_id=$2 AND assignment_id=$3`, projectID, course.ID, assignment.ID).Scan(&existing)
		if err == nil {
			result.Skipped++
			continue
		}
		if err != pgx.ErrNoRows {
			return result, err
		}
		var taskID int64
		err = tx.QueryRow(ctx, `INSERT INTO tasks(project_id,title,description,status,priority,due_date,author_id) VALUES($1,$2,$3,'backlog','medium',$4,$5) RETURNING id`, projectID, assignment.Name, assignment.Description, assignment.DueDate, actorID).Scan(&taskID)
		if err != nil {
			return result, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO akhcheck_imports(project_id,course_id,assignment_id,task_id) VALUES($1,$2,$3,$4)`, projectID, course.ID, assignment.ID, taskID); err != nil {
			return result, err
		}
		result.Created++
	}
	if err := tx.Commit(ctx); err != nil {
		return AkhcheckImportResult{}, err
	}
	return result, nil
}
