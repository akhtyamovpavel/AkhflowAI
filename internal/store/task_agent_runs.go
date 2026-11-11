package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrTaskRunAccess = errors.New("task access denied")

type TaskRunSnapshot struct {
	ID                 int64  `json:"id"`
	Title              string `json:"title"`
	Description        string `json:"description"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
}

func (s *Store) EnqueueTaskRun(ctx context.Context, taskID, actorID int64, key, message string) (*AgentRun, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(809020)`); err != nil {
		return nil, err
	}
	var snapshot TaskRunSnapshot
	var projectID int64
	err = tx.QueryRow(ctx, `SELECT id,project_id,title,description,acceptance_criteria FROM tasks WHERE id=$1 FOR UPDATE`, taskID).Scan(&snapshot.ID, &projectID, &snapshot.Title, &snapshot.Description, &snapshot.AcceptanceCriteria)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var role string
	err = tx.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2 FOR SHARE`, projectID, actorID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTaskRunAccess
	}
	if err != nil {
		return nil, err
	}
	if !CanWriteProject(role) {
		return nil, ErrTaskRunAccess
	}
	old, err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM agent_runs WHERE task_id=$1 AND actor_id=$2 AND request_key=$3::uuid`, taskID, actorID, key))
	if err == nil {
		if old.Message != message {
			return nil, ErrAgentRequestConflict
		}
		return old, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent_runs WHERE status='queued'`).Scan(&count); err != nil {
		return nil, err
	}
	if count >= 100 {
		return nil, ErrRunQueueFull
	}
	var chatID int64
	err = tx.QueryRow(ctx, `INSERT INTO agent_chats(project_id,user_id,title) VALUES($1,$2,$3) RETURNING id`, projectID, actorID, snapshot.Title).Scan(&chatID)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	query := `INSERT INTO agent_runs(chat_id,actor_id,request_key,message,history,status,task_id,task_snapshot,instructions_snapshot,instructions_version)
		SELECT $1,$2,$3::uuid,$4,'[]'::jsonb,'queued',$5,$6::jsonb,agent_instructions,agent_instructions_version FROM projects WHERE id=$7 RETURNING ` + runColumns
	run, err := scanRun(tx.QueryRow(ctx, query, chatID, actorID, key, message, taskID, encoded, projectID))
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent_messages(chat_id,role,content) VALUES($1,'user',$2)`, chatID, message); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *Store) TaskRunSnapshot(ctx context.Context, runID int64) (*TaskRunSnapshot, error) {
	var raw json.RawMessage
	if err := s.db.QueryRow(ctx, `SELECT task_snapshot FROM agent_runs WHERE id=$1`, runID).Scan(&raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	var snapshot TaskRunSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (s *Store) TaskRunIDs(ctx context.Context, taskID int64) ([]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM agent_runs WHERE task_id=$1 ORDER BY id DESC LIMIT 50`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
