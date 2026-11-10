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
		name := tool.Name
		tool.Handler = func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
			projectScoped, readOnly, known := toolAccessPolicy(name)
			if !known {
				return Result{}, errors.New("tool access policy missing")
			}
			if err := authorizeToolProject(ctx, deps, args, projectScoped, !readOnly); err != nil {
				return Result{}, err
			}
			return original(ctx, deps, args)
		}
	}
}

// New tools must explicitly declare their resource and whether they change it.
func toolAccessPolicy(name string) (projectScoped, readOnly, known bool) {
	switch name {
	case "list_tasks", "list_epics":
		return true, true, true
	case "get_task":
		return false, true, true
	case "create_task", "create_epic", "import_trello", "import_akhcheck_course", "import_bitrix24_tasks":
		return true, false, true
	case "move_task", "add_comment", "set_assignee", "set_task_epic", "add_dependency", "remove_dependency", "set_task_recurring", "delete_task":
		return false, false, true
	default:
		return false, false, false
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
