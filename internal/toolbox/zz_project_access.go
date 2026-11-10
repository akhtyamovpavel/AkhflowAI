package toolbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"akhflow.ai/akhflow/internal/store"
)

func init() {
	for i := range Registry {
		tool := &Registry[i]
		original := tool.Handler
		name := tool.Name
		schema := tool.Schema
		tool.Handler = func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
			projectScoped, readOnly, known := toolAccessPolicy(name)
			if !known {
				return Result{}, errors.New("tool access policy missing")
			}
			normalized, err := normalizeToolArguments(args, schema, projectScoped)
			if err != nil {
				return Result{}, err
			}
			resourceContext, err := authorizeToolProject(ctx, deps, normalized, projectScoped, !readOnly)
			if err != nil {
				return Result{}, err
			}
			return original(resourceContext, deps, normalized)
		}
	}
}

// normalizeToolArguments gives the authorization check and handler identical keys.
func normalizeToolArguments(args json.RawMessage, schema map[string]any, projectScoped bool) (json.RawMessage, error) {
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil, errors.New("tool argument schema missing")
	}
	resource := "task_id"
	if projectScoped {
		resource = "project"
	}
	if _, ok := properties[resource]; !ok {
		return nil, errors.New("tool resource schema missing")
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, errors.New("invalid tool arguments")
	}
	values := make(map[string]json.RawMessage)
	keys := []string{}
	for decoder.More() {
		if len(keys) >= 128 {
			return nil, errors.New("too many tool arguments")
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, errors.New("invalid tool arguments")
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid tool arguments")
		}
		for _, previous := range keys {
			if strings.EqualFold(previous, key) {
				return nil, errors.New("duplicate tool argument")
			}
		}
		keys = append(keys, key)
		canonical := key
		for property := range properties {
			if strings.EqualFold(property, key) {
				canonical = property
				break
			}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("invalid tool arguments")
		}
		values[canonical] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, errors.New("invalid tool arguments")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("invalid tool arguments")
	}
	return json.Marshal(values)
}

// New tools must explicitly declare their resource and whether they change it.
func toolAccessPolicy(name string) (projectScoped, readOnly, known bool) {
	switch name {
	case "list_tasks", "list_epics", "get_agent_usage":
		return true, true, true
	case "get_task", "list_attachments":
		return false, true, true
	case "create_task", "create_epic", "import_trello", "import_akhcheck_course", "import_bitrix24_tasks":
		return true, false, true
	case "move_task", "add_comment", "set_assignee", "set_task_epic", "add_dependency", "remove_dependency", "set_task_recurring", "delete_task":
		return false, false, true
	default:
		return false, false, false
	}
}

type authorizedTaskIDKey struct{}

func authorizedTaskID(ctx context.Context) (int64, error) {
	id, ok := ctx.Value(authorizedTaskIDKey{}).(int64)
	if !ok || id <= 0 {
		return 0, errors.New("task authorization missing")
	}
	return id, nil
}

func authorizeToolProject(ctx context.Context, deps Deps, args json.RawMessage, projectScoped, write bool) (context.Context, error) {
	var input map[string]json.RawMessage
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, errors.New("invalid tool arguments")
	}
	var projectID int64
	var taskID int64
	if projectScoped {
		var slug string
		if err := json.Unmarshal(input["project"], &slug); err != nil || slug == "" {
			return nil, errors.New("project or task not found")
		}
		project, err := deps.Store.ProjectBySlug(ctx, slug)
		if err != nil {
			return nil, err
		}
		if project != nil {
			projectID = project.ID
		}
	} else {
		if err := json.Unmarshal(input["task_id"], &taskID); err != nil || taskID <= 0 {
			return nil, errors.New("invalid task_id")
		}
		task, err := deps.Store.TaskByID(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if task != nil {
			projectID = task.ProjectID
		}
	}
	if projectID == 0 {
		return nil, errors.New("project or task not found")
	}
	role, err := deps.Store.ProjectRole(ctx, projectID, deps.ActorID)
	if err != nil {
		return nil, err
	}
	if !store.CanReadProject(role) || (write && !store.CanWriteProject(role)) {
		return nil, errors.New("access denied")
	}
	if !projectScoped {
		ctx = context.WithValue(ctx, authorizedTaskIDKey{}, taskID)
	}
	return ctx, nil
}
