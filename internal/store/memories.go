package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"akhflow.ai/akhflow/internal/domain"
)

func (s *Store) AddMemory(ctx context.Context, projectID *int64, userID int64, scope, content string) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `
		INSERT INTO memories(project_id, user_id, scope, content)
		VALUES($1,$2,$3,$4) RETURNING id`,
		projectID, userID, scope, content).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) ListMemories(ctx context.Context, projectID int64, userID int64) ([]domain.Memory, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, project_id, user_id, scope, content, created_at
		FROM memories
		WHERE (project_id = $1 AND scope = 'project') OR (user_id = $2 AND scope = 'user')
		ORDER BY id DESC`, projectID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Memory{}
	for rows.Next() {
		var m domain.Memory
		var projectID *int64
		if err := rows.Scan(&m.ID, &projectID, &m.UserID, &m.Scope, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.ProjectID = projectID
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) MemoryByID(ctx context.Context, id int64) (*domain.Memory, error) {
	var m domain.Memory
	var projectID *int64
	err := s.db.QueryRow(ctx, `
		SELECT id, project_id, user_id, scope, content, created_at
		FROM memories WHERE id=$1`, id).
		Scan(&m.ID, &projectID, &m.UserID, &m.Scope, &m.Content, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.ProjectID = projectID
	return &m, nil
}

func (s *Store) DeleteMemory(ctx context.Context, id, userID int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM memories WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

