package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type AgentInstructions struct {
	Instructions string     `json:"instructions"`
	Version      int64      `json:"version"`
	UpdatedAt    *time.Time `json:"updated_at"`
}

func (s *Store) AgentInstructions(ctx context.Context, projectID int64) (*AgentInstructions, error) {
	var value AgentInstructions
	err := s.db.QueryRow(ctx, `SELECT agent_instructions, agent_instructions_version, agent_instructions_updated_at FROM projects WHERE id=$1`, projectID).Scan(&value.Instructions, &value.Version, &value.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func (s *Store) UpdateAgentInstructions(ctx context.Context, projectID, actorID, version int64, instructions string) (*AgentInstructions, error) {
	var value AgentInstructions
	query := `UPDATE projects SET agent_instructions=$4, agent_instructions_version=agent_instructions_version+1, agent_instructions_updated_at=now()
        WHERE id=$1 AND agent_instructions_version=$3 AND EXISTS (SELECT 1 FROM project_members WHERE project_id=$1 AND user_id=$2 AND role IN ('owner','admin'))
        RETURNING agent_instructions, agent_instructions_version, agent_instructions_updated_at`
	err := s.db.QueryRow(ctx, query, projectID, actorID, version, instructions).Scan(&value.Instructions, &value.Version, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}
