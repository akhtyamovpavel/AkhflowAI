package store

import (
	"context"
	"fmt"
	"strings"

	"akhflow.ai/akhflow/internal/trello"

	"github.com/jackc/pgx/v5"
)

type TrelloImportResult struct {
	ListsCreated int `json:"lists_created"`
	TasksCreated int `json:"tasks_created"`
	TasksSkipped int `json:"tasks_skipped"`
}

// ImportTrello preserves local edits on repeat imports. A transaction and a
// project-scoped lock serialize concurrent imports and prevent duplicates.
func (s *Store) ImportTrello(ctx context.Context, projectID, actorID int64, board *trello.Board) (TrelloImportResult, error) {
	result := TrelloImportResult{}
	lists := make(map[string]bool, len(board.Lists))
	for _, list := range board.Lists {
		if list.ID == "" || strings.TrimSpace(list.Name) == "" || lists[list.ID] {
			return result, fmt.Errorf("Trello вернул некорректный список")
		}
		lists[list.ID] = true
	}
	cards := make(map[string]bool, len(board.Cards))
	for _, card := range board.Cards {
		if card.ID == "" || strings.TrimSpace(card.Name) == "" || !lists[card.ListID] || cards[card.ID] {
			return result, fmt.Errorf("Trello вернул некорректную карточку")
		}
		cards[card.ID] = true
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, projectID); err != nil {
		return result, err
	}
	epicIDs := make(map[string]int64, len(board.Lists))
	for _, list := range board.Lists {
		var epicID int64
		err := tx.QueryRow(ctx, `SELECT epic_id FROM trello_import_lists WHERE project_id=$1 AND board_id=$2 AND list_id=$3`, projectID, board.ID, list.ID).Scan(&epicID)
		if err == pgx.ErrNoRows {
			err = tx.QueryRow(ctx, `INSERT INTO epics(project_id,title,description,status) VALUES($1,$2,'','backlog') RETURNING id`, projectID, list.Name).Scan(&epicID)
			if err != nil {
				return result, err
			}
			_, err = tx.Exec(ctx, `INSERT INTO trello_import_lists(project_id,board_id,list_id,epic_id) VALUES($1,$2,$3,$4)`, projectID, board.ID, list.ID, epicID)
			if err != nil {
				return result, err
			}
			result.ListsCreated++
		} else if err != nil {
			return result, err
		}
		epicIDs[list.ID] = epicID
	}
	for _, card := range board.Cards {
		var exists bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM trello_import_cards WHERE project_id=$1 AND board_id=$2 AND card_id=$3)`, projectID, board.ID, card.ID).Scan(&exists)
		if err != nil {
			return result, err
		}
		if exists {
			result.TasksSkipped++
			continue
		}
		var taskID int64
		err = tx.QueryRow(ctx, `INSERT INTO tasks(project_id,title,description,status,priority,due_date,author_id,epic_id) VALUES($1,$2,$3,'backlog','medium',$4,$5,$6) RETURNING id`, projectID, card.Name, card.Desc, card.Due, actorID, epicIDs[card.ListID]).Scan(&taskID)
		if err != nil {
			return result, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO trello_import_cards(project_id,board_id,card_id,task_id) VALUES($1,$2,$3,$4)`, projectID, board.ID, card.ID, taskID)
		if err != nil {
			return result, err
		}
		result.TasksCreated++
	}
	if err := tx.Commit(ctx); err != nil {
		return TrelloImportResult{}, err
	}
	return result, nil
}
