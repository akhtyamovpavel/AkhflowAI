package toolbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"akhflow.ai/akhflow/internal/bitrix24"
	"akhflow.ai/akhflow/internal/domain"
	"akhflow.ai/akhflow/internal/events"
)

func init() {
	Registry = append(Registry, Tool{
		Name:        "import_bitrix24_tasks",
		Description: "Import accessible Bitrix24 tasks into a project; repeated imports skip existing tasks",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project":  map[string]any{"type": "string"},
				"group_id": map[string]any{"type": "integer", "minimum": 0, "description": "Bitrix24 group ID, or 0 for all accessible tasks"},
			},
			"required": []string{"project"},
		},
		Handler: importBitrixTasks,
	})
}

func importBitrixTasks(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
	var input struct {
		Project string `json:"project"`
		GroupID int64  `json:"group_id"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return Result{}, errors.New("invalid import parameters")
	}
	p, err := projectBySlug(ctx, d, input.Project)
	if err != nil {
		return Result{}, err
	}
	webhook := os.Getenv("BITRIX24_WEBHOOK_URL")
	if webhook == "" {
		return Result{}, errors.New("Bitrix24 webhook is not configured")
	}
	allowedID, err := strconv.ParseInt(os.Getenv("BITRIX24_ALLOWED_USER_ID"), 10, 64)
	if err != nil || allowedID <= 0 || allowedID != d.ActorID {
		return Result{}, errors.New("Bitrix24 import is not enabled for this user")
	}
	client := bitrix24.Client{URL: webhook}
	portal, tasks, err := client.Fetch(ctx, input.GroupID)
	if err != nil {
		return Result{}, err
	}
	created := 0
	skipped := 0
	defer func() {
		if created > 0 {
			publishEvent(d, events.Event{Type: "tasks_imported", ProjectID: p.ID, Actor: d.Actor})
		}
	}()
	for _, source := range tasks {
		task := domain.Task{ProjectID: p.ID, AuthorID: d.ActorID, Title: source.Title, Description: source.Description, Status: source.Status, Priority: source.Priority, DueDate: source.DueDate}
		inserted, err := d.Store.ImportBitrixTask(ctx, portal, source.ID, task)
		if err != nil {
			return Result{}, errors.New("import failed; retry to continue")
		}
		if inserted {
			created++
		} else {
			skipped++
		}
	}
	message := fmt.Sprintf("Импортировано: %d, пропущено: %d", created, skipped)
	return Result{Ok: true, Message: message, Data: map[string]int{"created": created, "skipped": skipped}}, nil
}
