package toolbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"akhflow.ai/akhflow/internal/trello"
)

var ErrTrelloAccess = errors.New("импорт Trello недоступен для этого пользователя")

func init() {
	Registry = append(Registry, Tool{
		Name:        "import_trello",
		Description: "Import open Trello lists as epics and cards as backlog tasks; repeat imports skip existing cards. Uses server-side Trello credentials.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project":  map[string]any{"type": "string", "description": "Project space slug"},
				"board_id": map[string]any{"type": "string", "description": "Trello board ID or short ID"},
			},
			"required": []string{"project", "board_id"},
		},
		Handler: importTrello,
	})
}

func importTrello(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
	var input struct {
		Project string `json:"project"`
		BoardID string `json:"board_id"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return Result{}, errors.New("некорректные параметры импорта")
	}
	project, err := projectBySlug(ctx, d, input.Project)
	if err != nil {
		return Result{}, err
	}
	allowedID, err := strconv.ParseInt(os.Getenv("TRELLO_IMPORT_USER_ID"), 10, 64)
	if err != nil || allowedID != d.ActorID || allowedID <= 0 {
		return Result{}, ErrTrelloAccess
	}
	client := trello.New(os.Getenv("TRELLO_API_KEY"), os.Getenv("TRELLO_API_TOKEN"))
	board, err := client.FetchBoard(ctx, input.BoardID)
	if err != nil {
		return Result{}, err
	}
	imported, err := d.Store.ImportTrello(ctx, project.ID, d.ActorID, board)
	if err != nil {
		return Result{}, errors.New("не удалось сохранить импорт Trello")
	}
	message := fmt.Sprintf("Импортировано задач: %d, пропущено существующих: %d, создано эпиков: %d", imported.TasksCreated, imported.TasksSkipped, imported.ListsCreated)
	if imported.TasksUngrouped > 0 {
		message += fmt.Sprintf(", без эпика: %d", imported.TasksUngrouped)
	}
	return Result{Ok: true, Message: message, Data: imported}, nil
}
