package toolbox

import (
	"context"
	"encoding/json"
	"fmt"

	"akhflow.ai/akhflow/internal/akhcheck"
	"akhflow.ai/akhflow/internal/events"
)

func init() {
	Registry = append(Registry, Tool{
		Name:        "import_akhcheck_course",
		Description: "Import tasks from a saved Akhcheck course-detail JSON response. Existing assignments are skipped.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project": map[string]any{"type": "string"},
				"course":  map[string]any{"type": "object", "description": "Akhcheck course-detail JSON with id and tasks"},
			},
			"required": []string{"project", "course"},
		},
		Handler: importAkhcheckCourse,
	})
}

func importAkhcheckCourse(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
	var input struct {
		Project string          `json:"project"`
		Course  json.RawMessage `json:"course"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return Result{}, err
	}
	project, err := projectBySlug(ctx, deps, input.Project)
	if err != nil {
		return Result{}, err
	}
	course, err := akhcheck.ParseCourse(input.Course)
	if err != nil {
		return Result{}, err
	}
	result, err := deps.Store.ImportAkhcheck(ctx, project.ID, deps.ActorID, course)
	if err != nil {
		return Result{}, err
	}
	if result.Created > 0 {
		publishEvent(deps, events.Event{Type: "tasks_imported", ProjectID: project.ID, Actor: deps.Actor})
	}
	return Result{Ok: true, Message: fmt.Sprintf("Импортировано: %d, пропущено: %d", result.Created, result.Skipped), Data: result}, nil
}
