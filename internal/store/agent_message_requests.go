package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrAgentRequestConflict = errors.New("idempotency key already used with another message")

// ClaimAgentMessage commits the request and its user message together. A nil
// result for an existing request means it is still running or its outcome is unknown.
func (s *Store) ClaimAgentMessage(ctx context.Context, chatID, actorID int64, key, message, title string) (bool, json.RawMessage, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, nil, err
	}
	defer tx.Rollback(ctx)
	command, err := tx.Exec(ctx, `INSERT INTO agent_message_requests(chat_id, actor_id, request_key, message)
        VALUES($1,$2,$3::uuid,$4) ON CONFLICT DO NOTHING`, chatID, actorID, key, message)
	if err != nil {
		return false, nil, err
	}
	if command.RowsAffected() == 0 {
		var original string
		var result json.RawMessage
		err = tx.QueryRow(ctx, `SELECT message, result FROM agent_message_requests
            WHERE chat_id=$1 AND actor_id=$2 AND request_key=$3::uuid`, chatID, actorID, key).Scan(&original, &result)
		if err != nil {
			return false, nil, err
		}
		if original != message {
			return false, nil, ErrAgentRequestConflict
		}
		return false, result, nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO agent_messages(chat_id, role, content) VALUES($1,'user',$2)`, chatID, message)
	if err != nil {
		return false, nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE agent_chats SET title=$2 WHERE id=$1 AND title=''`, chatID, title)
	if err != nil {
		return false, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, nil, err
	}
	return true, nil, nil
}

// FinishAgentMessage commits the answer and the replayable result together.
func (s *Store) FinishAgentMessage(ctx context.Context, chatID, actorID int64, key, reply string, toolCalls any, result any) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var calls any
	if toolCalls != nil {
		calls, err = json.Marshal(toolCalls)
		if err != nil {
			return err
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	command, err := tx.Exec(ctx, `UPDATE agent_message_requests SET result=$4::jsonb
        WHERE chat_id=$1 AND actor_id=$2 AND request_key=$3::uuid AND result IS NULL`, chatID, actorID, key, encoded)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	_, err = tx.Exec(ctx, `INSERT INTO agent_messages(chat_id, role, content, tool_calls) VALUES($1,'agent',$2,$3)`, chatID, reply, calls)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
