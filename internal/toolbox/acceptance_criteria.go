package toolbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"akhflow.ai/akhflow/internal/domain"
	"akhflow.ai/akhflow/internal/events"
)

func init() {
	Registry = append(Registry, Tool{
		Name:        "set_task_acceptance_criteria",
		Description: "Set or clear the acceptance criteria for a task",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":             map[string]any{"type": "integer"},
				"acceptance_criteria": map[string]any{"type": "string"},
			},
			"required": []string{"task_id", "acceptance_criteria"},
		},
		Handler: setTaskAcceptanceCriteria,
	})
}

func setTaskAcceptanceCriteria(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
	var values map[string]any
	if err := json.Unmarshal(args, &values); err != nil {
		return Result{}, err
	}
	criteria, ok := values["acceptance_criteria"].(string)
	if !ok || !domain.ValidAcceptanceCriteria(criteria) {
		return Result{}, errors.New("invalid acceptance criteria")
	}
	id, err := authorizedTaskID(ctx)
	if err != nil {
		return Result{}, err
	}
	task, err := d.Store.TaskByID(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if task == nil {
		return Result{}, errors.New("task not found")
	}
	if err = d.Store.UpdateTask(ctx, id, []string{"acceptance_criteria"}, []any{criteria}); err != nil {
		return Result{}, err
	}
	publishEvent(d, events.Event{Type: "task_updated", ProjectID: task.ProjectID, TaskID: id, Title: task.Title, Actor: d.Actor})
	return Result{Ok: true, Message: fmt.Sprintf("task %d acceptance criteria updated", id)}, nil
}
