package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrRunConflict = errors.New("run conflict")
var ErrRunQueueFull = errors.New("run queue full")

type AgentRun struct {
	ID                   int64            `json:"id"`
	ChatID               int64            `json:"chat_id"`
	TaskID               *int64           `json:"task_id"`
	ActorID              int64            `json:"actor_id"`
	RequestKey           string           `json:"request_key"`
	Message              string           `json:"message"`
	History              json.RawMessage  `json:"-"`
	InstructionsSnapshot string           `json:"-"`
	InstructionsVersion  int64            `json:"instructions_version"`
	Status               string           `json:"status"`
	Iteration            int              `json:"-"`
	Tool                 *string          `json:"-"`
	Step                 AgentRunProgress `json:"step"`
	Result               json.RawMessage  `json:"result"`
	ErrorCode            *string          `json:"error_code"`
	CreatedAt            time.Time        `json:"created_at"`
	StartedAt            *time.Time       `json:"started_at"`
	FinishedAt           *time.Time       `json:"finished_at"`
	CancelRequested      bool             `json:"cancel_requested"`
	Steps                []AgentRunStep   `json:"steps"`
}
type AgentRunProgress struct {
	Iteration     int     `json:"iteration"`
	MaxIterations int     `json:"max_iterations"`
	Tool          *string `json:"tool,omitempty"`
}
type AgentRunStep struct {
	Sequence   int        `json:"sequence"`
	Iteration  int        `json:"iteration"`
	Tool       string     `json:"tool"`
	Status     string     `json:"status"`
	Summary    string     `json:"summary"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

const runColumns = `id,chat_id,task_id,actor_id,request_key::text,message,history,status,iteration,tool,result,error_code,created_at,started_at,finished_at,cancel_requested,instructions_snapshot,instructions_version`

func scanRun(row pgx.Row) (*AgentRun, error) {
	r := &AgentRun{Steps: []AgentRunStep{}}
	err := row.Scan(&r.ID, &r.ChatID, &r.TaskID, &r.ActorID, &r.RequestKey, &r.Message, &r.History, &r.Status, &r.Iteration, &r.Tool, &r.Result, &r.ErrorCode, &r.CreatedAt, &r.StartedAt, &r.FinishedAt, &r.CancelRequested, &r.InstructionsSnapshot, &r.InstructionsVersion)

	if err != nil {
		return nil, err
	}
	r.Step = AgentRunProgress{Iteration: r.Iteration, MaxIterations: 8, Tool: r.Tool}
	return r, nil
}

func (s *Store) EnqueueRun(ctx context.Context, chatID, actorID int64, key, message, title string) (*AgentRun, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Serialize queue capacity and history snapshots across all API processes.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(809020)`); err != nil {
		return nil, err
	}
	old, err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM agent_runs WHERE chat_id=$1 AND actor_id=$2 AND request_key=$3::uuid`, chatID, actorID, key))
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
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE chat_id=$1 AND status IN ('queued','running'))`, chatID).Scan(&active); err != nil {
		return nil, err
	}
	if active {
		return nil, ErrRunConflict
	}
	var history json.RawMessage
	query := `SELECT COALESCE(jsonb_agg(jsonb_build_object('role', role, 'content', content) ORDER BY id),'[]'::jsonb) FROM (SELECT id,role,content FROM agent_messages WHERE chat_id=$1 ORDER BY id DESC LIMIT 20) m`
	if err = tx.QueryRow(ctx, query, chatID).Scan(&history); err != nil {
		return nil, err
	}
	query = `INSERT INTO agent_runs(chat_id,actor_id,request_key,message,history,status,instructions_snapshot,instructions_version) SELECT $1,$2,$3::uuid,$4,$5,'queued',p.agent_instructions,p.agent_instructions_version FROM agent_chats c JOIN projects p ON p.id=c.project_id WHERE c.id=$1 RETURNING ` + runColumns
	run, err := scanRun(tx.QueryRow(ctx, query, chatID, actorID, key, message, history))
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent_messages(chat_id,role,content) VALUES($1,'user',$2)`, chatID, message); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_chats SET title=$2 WHERE id=$1 AND title=''`, chatID, title); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return run, nil
}
func (s *Store) AgentRun(ctx context.Context, id int64) (*AgentRun, error) {
	run, err := scanRun(s.db.QueryRow(ctx, `SELECT `+runColumns+` FROM agent_runs WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT sequence,iteration,tool,status,summary,started_at,finished_at FROM agent_run_steps WHERE run_id=$1 ORDER BY sequence`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var step AgentRunStep
		if err = rows.Scan(&step.Sequence, &step.Iteration, &step.Tool, &step.Status, &step.Summary, &step.StartedAt, &step.FinishedAt); err != nil {
			return nil, err
		}
		run.Steps = append(run.Steps, step)
	}
	return run, rows.Err()
}
func (s *Store) AgentRuns(ctx context.Context, chatID int64, active bool) ([]AgentRun, error) {
	query := `SELECT ` + runColumns + ` FROM agent_runs WHERE chat_id=$1 AND (NOT $2 OR status IN ('queued','running')) ORDER BY id DESC LIMIT 50`
	rows, err := s.db.Query(ctx, query, chatID, active)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentRun{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	return out, rows.Err()
}

func (s *Store) ClaimRun(ctx context.Context, token string) (*AgentRun, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(809020)`); err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE agent_runs SET status='interrupted',error_code='worker_lost',finished_at=now(),lease_token=NULL,lease_until=NULL WHERE status='running' AND lease_until < now()`)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE agent_run_steps SET status='interrupted',summary='Результат действия не подтверждён',finished_at=now() WHERE status='running' AND run_id IN (SELECT id FROM agent_runs WHERE status='interrupted')`)
	if err != nil {
		return nil, err
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE status='running')`).Scan(&active); err != nil {
		return nil, err
	}
	if active {
		return nil, tx.Commit(ctx)
	}
	query := `UPDATE agent_runs SET status='running',started_at=now(),lease_token=$1::uuid,lease_until=now()+interval '30 seconds' WHERE id=(SELECT id FROM agent_runs WHERE status='queued' ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING ` + runColumns
	run, err := scanRun(tx.QueryRow(ctx, query, token))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return run, nil
}
func (s *Store) HeartbeatRun(ctx context.Context, id int64, token string) (bool, bool, error) {
	var cancel bool
	err := s.db.QueryRow(ctx, `UPDATE agent_runs SET lease_until=now()+interval '30 seconds' WHERE id=$1 AND status='running' AND lease_token=$2::uuid AND lease_until>now() RETURNING cancel_requested`, id, token).Scan(&cancel)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	return err == nil, cancel, err
}
func (s *Store) CancelRun(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `UPDATE agent_runs SET cancel_requested=true,status=CASE WHEN status='queued' THEN 'canceled' ELSE status END,finished_at=CASE WHEN status='queued' THEN now() ELSE finished_at END WHERE id=$1 AND status IN ('queued','running')`, id)
	return err
}
func (s *Store) StartRunStep(ctx context.Context, id int64, token string, sequence, iteration int, tool string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	cmd, err := tx.Exec(ctx, `UPDATE agent_runs SET iteration=$3,tool=$4 WHERE id=$1 AND lease_token=$2::uuid AND status='running' AND lease_until>now() AND NOT cancel_requested`, id, token, iteration, tool)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() != 1 {
		return errors.New("run lease lost")
	}
	_, err = tx.Exec(ctx, `INSERT INTO agent_run_steps(run_id,sequence,iteration,tool,status) VALUES($1,$2,$3,$4,'running')`, id, sequence, iteration, tool)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) FinishRunStep(ctx context.Context, id int64, token string, sequence int, status, summary string) error {
	query := `UPDATE agent_run_steps SET status=$4,summary=$5,finished_at=now() WHERE run_id=$1 AND sequence=$3 AND EXISTS(SELECT 1 FROM agent_runs WHERE id=$1 AND lease_token=$2::uuid AND status='running' AND lease_until>now())`
	cmd, err := s.db.Exec(ctx, query, id, token, sequence, status, summary)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() != 1 {
		return errors.New("run lease lost")
	}
	return nil
}
func (s *Store) FinishRun(ctx context.Context, id int64, token, status, code string, result any, reply string, calls any) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var encoded any
	if result != nil {
		encoded, err = json.Marshal(result)
		if err != nil {
			return err
		}
	}
	var errorCode any
	if code != "" {
		errorCode = code
	}
	query := `UPDATE agent_runs SET status=$3,error_code=$4,result=$5::jsonb,iteration=COALESCE(($5::jsonb->>'iterations')::integer,iteration),finished_at=now(),lease_token=NULL,lease_until=NULL,tool=NULL WHERE id=$1 AND lease_token=$2::uuid AND status='running' AND lease_until>now() RETURNING chat_id,status`
	var chatID int64
	var finalStatus string
	if err = tx.QueryRow(ctx, query, id, token, status, errorCode, encoded).Scan(&chatID, &finalStatus); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE agent_run_steps SET status='interrupted',summary='Результат действия не подтверждён',finished_at=now() WHERE run_id=$1 AND status='running'`, id)
	if err != nil {
		return err
	}
	if result != nil && finalStatus != "canceled" {
		encodedCalls, err := json.Marshal(calls)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent_messages(chat_id,role,content,tool_calls) VALUES($1,'agent',$2,$3)`, chatID, reply, encodedCalls); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
