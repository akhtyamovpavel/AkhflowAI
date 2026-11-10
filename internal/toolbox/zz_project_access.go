package toolbox

import (
	"context"
	"encoding/json"
	"errors"

	"akhflow.ai/akhflow/internal/store"
)

func init() {
	for i := range Registry {
		tool := &Registry[i]
		original := tool.Handler
		readOnly := tool.Name == "list_tasks" || tool.Name == "get_task" || tool.Name == "list_epics"
		projectScoped := tool.Name == "list_tasks" || tool.Name == "create_task" || tool.Name == "list_epics" || tool.Name == "create_epic" || tool.Name == "import_trello" || tool.Name == "import_akhcheck_course" || tool.Name == "import_bitrix24_tasks"
		tool.Handler = func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
			if err := authorizeToolProject(ctx, deps, args, projectScoped, !readOnly); err != nil {
				return Result{}, err
			}
			return original(ctx, deps, args)
		}
	}
}

func authorizeToolProject(ctx context.Context, deps Deps, args json.RawMessage, projectScoped, write bool) error {
	var input struct {
		Project string `json:"project"`
		TaskID  int64  `json:"task_id"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errors.New("invalid tool arguments")
	}
	var projectID int64
	if projectScoped && input.Project != "" {
		project, err := deps.Store.ProjectBySlug(ctx, input.Project)
		if err != nil {
			return err
		}
		if project != nil {
			projectID = project.ID
		}
	} else if !projectScoped && input.TaskID > 0 {
		task, err := deps.Store.TaskByID(ctx, input.TaskID)
		if err != nil {
			return err
		}
		if task != nil {
			projectID = task.ProjectID
		}
	}
	if projectID == 0 {
		return errors.New("project or task not found")
	}
	role, err := deps.Store.ProjectRole(ctx, projectID, deps.ActorID)
	if err != nil {
		return err
	}
	if !store.CanReadProject(role) || (write && !store.CanWriteProject(role)) {
		return errors.New("access denied")
	}
	return nil
}
