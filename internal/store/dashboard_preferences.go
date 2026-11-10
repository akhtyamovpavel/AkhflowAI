package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (s *Store) DashboardHiddenWidgets(ctx context.Context, projectID, userID int64) ([]string, error) {
	widgets := []string{}
	err := s.db.QueryRow(ctx, `SELECT hidden_widgets FROM dashboard_preferences WHERE project_id=$1 AND user_id=$2`, projectID, userID).Scan(&widgets)
	if errors.Is(err, pgx.ErrNoRows) {
		return widgets, nil
	}
	return widgets, err
}

func (s *Store) SetDashboardHiddenWidgets(ctx context.Context, projectID, userID int64, widgets []string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO dashboard_preferences (project_id, user_id, hidden_widgets)
        VALUES ($1, $2, $3) ON CONFLICT (project_id, user_id) DO UPDATE SET hidden_widgets=EXCLUDED.hidden_widgets`, projectID, userID, widgets)
	return err
}
