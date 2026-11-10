package store

import (
	"context"
	"fmt"
)

// AgentUsage summarizes persisted project chat messages for a rolling period.
type AgentUsage struct {
	Days      int   `json:"days"`
	Requests  int64 `json:"requests"`
	Replies   int64 `json:"replies"`
	ToolCalls int64 `json:"tool_calls"`
}

func ValidAgentUsageDays(days int) bool {
	return days == 0 || days == 7 || days == 30
}

func (s *Store) AgentUsage(ctx context.Context, projectID int64, days int) (AgentUsage, error) {
	result := AgentUsage{Days: days}
	if !ValidAgentUsageDays(days) {
		return result, fmt.Errorf("days must be 0, 7 or 30")
	}
	query := `SELECT
		count(*) FILTER (WHERE m.role = 'user'),
		count(*) FILTER (WHERE m.role = 'agent'),
		coalesce(sum(CASE WHEN m.role = 'agent' AND jsonb_typeof(m.tool_calls) = 'array'
			THEN jsonb_array_length(m.tool_calls) ELSE 0 END), 0)
		FROM agent_chats c JOIN agent_messages m ON m.chat_id = c.id
		WHERE c.project_id = $1 AND ($2::integer = 0 OR m.created_at >= now() - $2 * interval '1 day')`
	err := s.db.QueryRow(ctx, query, projectID, days).Scan(
		&result.Requests,
		&result.Replies,
		&result.ToolCalls,
	)
	return result, err
}
