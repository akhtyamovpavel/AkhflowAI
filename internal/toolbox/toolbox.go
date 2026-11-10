package toolbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"akhflow.ai/akhflow/internal/domain"
	"akhflow.ai/akhflow/internal/events"
	"akhflow.ai/akhflow/internal/store"
)

type Deps struct {
	Store   *store.Store
	ActorID int64
	Actor   string
	Bus     *events.Bus
}

type Result struct {
	Ok      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Handler     func(ctx context.Context, d Deps, args json.RawMessage) (Result, error)
}

func str(v map[string]any, key string) string {
	if s, ok := v[key].(string); ok {
		return s
	}
	return ""
}

func num(v map[string]any, key string) *int {
	if f, ok := v[key].(float64); ok {
		n := int(f)
		return &n
	}
	return nil
}

func strPtr(v map[string]any, key string) *string {
	if s, ok := v[key].(string); ok && s != "" {
		return &s
	}
	return nil
}

func datePtr(v map[string]any, key string) *time.Time {
	s := str(v, key)
	if s == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil
	}
	return &t
}

func projectBySlug(ctx context.Context, d Deps, slug string) (*domain.Project, error) {
	if slug == "" {
		return nil, errors.New("project slug is required")
	}
	p, err := d.Store.ProjectBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("project %q not found", slug)
	}
	member, err := d.Store.IsMember(ctx, p.ID, d.ActorID)
	if err != nil {
		return nil, err
	}
	if !member {
		return nil, fmt.Errorf("access denied to project %q", slug)
	}
	return p, nil
}

func taskForMember(ctx context.Context, d Deps, id int64) (*domain.Task, error) {
	task, err := d.Store.TaskByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("task %d not found", id)
	}
	member, err := d.Store.IsMember(ctx, task.ProjectID, d.ActorID)
	if err != nil {
		return nil, err
	}
	if !member {
		return nil, fmt.Errorf("access denied to task %d", id)
	}
	return task, nil
}

func validStatus(s string) bool {
	for _, v := range domain.Statuses {
		if v == s {
			return true
		}
	}
	return false
}

func validPriority(s string) bool {
	for _, v := range domain.Priorities {
		if v == s {
			return true
		}
	}
	return false
}

func validRecurring(s string) bool {
	return domain.ValidRecurring(s)
}

// publishEvent publishes an event if bus is not nil.
func publishEvent(d Deps, e events.Event) {
	if d.Bus != nil {
		d.Bus.Publish(e)
	}
}

// SpawnNextOccurrence creates the next occurrence of a recurring task.
// When inheritRecurring is true the new task continues the chain (completion hook);
// otherwise it is a one-off occurrence (scheduler).
func SpawnNextOccurrence(ctx context.Context, d Deps, orig *domain.Task, inheritRecurring bool) (int64, error) {
	next := domain.Task{
		ProjectID:   orig.ProjectID,
		ParentID:    orig.ParentID,
		Title:       orig.Title,
		Description: orig.Description,
		Status:      domain.StatusTodo,
		Priority:    orig.Priority,
		EstimateMin: orig.EstimateMin,
		AuthorID:    orig.AuthorID,
		AssigneeID:  orig.AssigneeID,
		Labels:      orig.Labels,
	}
	if inheritRecurring {
		next.Recurring = orig.Recurring
	}
	if orig.DueDate != nil {
		if step, ok := domain.RecurrenceStep(orig.Recurring); ok {
			nd := step(*orig.DueDate)
			next.DueDate = &nd
		}
	}
	id, err := d.Store.CreateTask(ctx, next)
	if err != nil {
		return 0, err
	}
	eid := id
	if err := d.Store.Audit(ctx, d.Actor, "recur_task", "task", &eid, map[string]any{"from": orig.ID}); err != nil {
		return 0, err
	}
	if d.ActorID != 0 {
		if _, err := d.Store.AddComment(ctx, orig.ID, d.ActorID, fmt.Sprintf("повторение: создана задача #%d", id)); err != nil {
			return 0, err
		}
	}
	publishEvent(d, events.Event{Type: "task_created", ProjectID: orig.ProjectID, TaskID: id, Title: orig.Title, Actor: d.Actor})
	return id, nil
}

var Registry = []Tool{
	{
		Name:        "list_attachments",
		Description: "List uploaded attachments on a task accessible to the current user",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "integer"},
			},
			"required": []string{"task_id"},
		},
		Handler: listAttachments,
	},
	{
		Name:        "list_tasks",
		Description: "List tasks in a project space, optionally filtered by status (backlog, todo, in_progress, done). Use brief=true to save tokens: returns only id, title, status, priority",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project": map[string]any{"type": "string", "description": "Project space slug"},
				"status":  map[string]any{"type": "string", "enum": domain.Statuses},
				"brief":   map[string]any{"type": "boolean", "description": "Return only id, title, status, priority"},
			},
			"required": []string{"project"},
		},
		Handler: func(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
			var v map[string]any
			if err := json.Unmarshal(args, &v); err != nil {
				return Result{}, err
			}
			p, err := projectBySlug(ctx, d, str(v, "project"))
			if err != nil {
				return Result{}, err
			}
			status := str(v, "status")
			if status != "" && !validStatus(status) {
				return Result{}, fmt.Errorf("unknown status %q", status)
			}
			tasks, err := d.Store.ListTasks(ctx, p.ID, status)
			if err != nil {
				return Result{}, err
			}
			if brief, _ := v["brief"].(bool); brief {
				out := make([]map[string]any, 0, len(tasks))
				for _, t := range tasks {
					out = append(out, map[string]any{
						"id": t.ID, "title": t.Title, "status": t.Status, "priority": t.Priority,
					})
				}
				return Result{Ok: true, Data: out}, nil
			}
			return Result{Ok: true, Data: tasks}, nil
		},
	},
	{
		Name:        "create_task",
		Description: "Create a new task in a project space",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project":     map[string]any{"type": "string", "description": "Project space slug"},
				"title":       map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"status":      map[string]any{"type": "string", "enum": domain.Statuses},
				"priority":    map[string]any{"type": "string", "enum": domain.Priorities},
				"due_date":    map[string]any{"type": "string", "format": "date", "description": "YYYY-MM-DD"},
				"assignee":    map[string]any{"type": "string", "description": "Assignee email"},
				"labels":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"recurring":   map[string]any{"type": "string", "enum": append(domain.RecurringSpecs, ""), "description": "daily, weekly or monthly"},
			},
			"required": []string{"project", "title"},
		},
		Handler: func(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
			var v map[string]any
			if err := json.Unmarshal(args, &v); err != nil {
				return Result{}, err
			}
			p, err := projectBySlug(ctx, d, str(v, "project"))
			if err != nil {
				return Result{}, err
			}
			title := str(v, "title")
			if title == "" {
				return Result{}, errors.New("title is required")
			}
			t := domain.Task{
				ProjectID:   p.ID,
				Title:       title,
				Description: str(v, "description"),
				Status:      domain.StatusTodo,
				Priority:    domain.PriorityMedium,
				AuthorID:    d.ActorID,
				DueDate:     datePtr(v, "due_date"),
			}
			if s := str(v, "status"); s != "" {
				if !validStatus(s) {
					return Result{}, fmt.Errorf("unknown status %q", s)
				}
				t.Status = s
			}
			if pr := str(v, "priority"); pr != "" {
				if !validPriority(pr) {
					return Result{}, fmt.Errorf("unknown priority %q", pr)
				}
				t.Priority = pr
			}
			if em := str(v, "assignee"); em != "" {
				u, err := d.Store.UserByEmail(ctx, em)
				if err != nil {
					return Result{}, err
				}
				if u == nil {
					return Result{}, fmt.Errorf("assignee %q not found", em)
				}
				if err := d.Store.ValidateAssignee(ctx, p.ID, u.ID); err != nil {
					return Result{}, err
				}
				t.AssigneeID = &u.ID
			}
			if raw, ok := v["labels"].([]any); ok {
				for _, l := range raw {
					if s, ok := l.(string); ok {
						t.Labels = append(t.Labels, s)
					}
				}
			}
			if rc := str(v, "recurring"); rc != "" {
				if !validRecurring(rc) {
					return Result{}, fmt.Errorf("unknown recurring %q", rc)
				}
				t.Recurring = rc
			}
			id, err := d.Store.CreateTask(ctx, t)
			if err != nil {
				return Result{}, err
			}
			pid := id
			if err := d.Store.Audit(ctx, d.Actor, "create_task", "task", &pid, map[string]any{"title": title, "project": p.Slug}); err != nil {
				return Result{}, err
			}
			publishEvent(d, events.Event{Type: "task_created", ProjectID: p.ID, TaskID: id, Title: title, Actor: d.Actor})
			return Result{Ok: true, Message: fmt.Sprintf("task %d created", id), Data: map[string]any{"id": id}}, nil
		},
	},
	{
		Name:        "move_task",
		Description: "Move a task to another status column",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "integer"},
				"status":  map[string]any{"type": "string", "enum": domain.Statuses},
			},
			"required": []string{"task_id", "status"},
		},
		Handler: func(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
			var v map[string]any
			if err := json.Unmarshal(args, &v); err != nil {
				return Result{}, err
			}
			idf, ok := v["task_id"].(float64)
			if !ok {
				return Result{}, errors.New("task_id is required")
			}
			id := int64(idf)
			status := str(v, "status")
			if !validStatus(status) {
				return Result{}, fmt.Errorf("unknown status %q", status)
			}
			t, err := d.Store.TaskByID(ctx, id)
			if err != nil {
				return Result{}, err
			}
			if t == nil {
				return Result{}, fmt.Errorf("task %d not found", id)
			}
			member, err := d.Store.IsMember(ctx, t.ProjectID, d.ActorID)
			if err != nil {
				return Result{}, err
			}
			if !member {
				return Result{}, fmt.Errorf("access denied to task %d", id)
			}
			if status == domain.StatusDone {
				blocked, err := d.Store.UnfinishedDependencies(ctx, id)
				if err != nil {
					return Result{}, err
				}
				if len(blocked) > 0 {
					titles := make([]string, 0, len(blocked))
					for _, b := range blocked {
						titles = append(titles, fmt.Sprintf("#%d %s", b.ID, b.Title))
					}
					return Result{}, fmt.Errorf("task %d has unfinished dependencies: %s", id, strings.Join(titles, ", "))
				}
				if t.Recurring != "" {
					if _, err := SpawnNextOccurrence(ctx, d, t, true); err != nil {
						return Result{}, err
					}
				}
			}
			if err := d.Store.UpdateTask(ctx, id, []string{"status"}, []any{status}); err != nil {
				return Result{}, err
			}
			eid := id
			if err := d.Store.Audit(ctx, d.Actor, "move_task", "task", &eid, map[string]any{"status": status}); err != nil {
				return Result{}, err
			}
			publishEvent(d, events.Event{Type: "task_moved", ProjectID: t.ProjectID, TaskID: id, Title: t.Title, Status: status, Actor: d.Actor})
			return Result{Ok: true, Message: fmt.Sprintf("task %d moved to %s", id, status)}, nil
		},
	},
	{
		Name:        "get_task",
		Description: "Get a task with details, labels, comments and audit history",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "integer"},
			},
			"required": []string{"task_id"},
		},
		Handler: func(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
			var v map[string]any
			if err := json.Unmarshal(args, &v); err != nil {
				return Result{}, err
			}
			idf, ok := v["task_id"].(float64)
			if !ok {
				return Result{}, errors.New("task_id is required")
			}
			id := int64(idf)
			t, err := taskForMember(ctx, d, id)
			if err != nil {
				return Result{}, err
			}
			comments, err := d.Store.ListComments(ctx, id)
			if err != nil {
				return Result{}, err
			}
			history, err := d.Store.AuditForTask(ctx, id, 50)
			if err != nil {
				return Result{}, err
			}
			return Result{Ok: true, Data: map[string]any{"task": t, "comments": comments, "history": history}}, nil
		},
	},
	{
		Name:        "add_comment",
		Description: "Add a comment to a task",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "integer"},
				"body":    map[string]any{"type": "string"},
			},
			"required": []string{"task_id", "body"},
		},
		Handler: func(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
			var v map[string]any
			if err := json.Unmarshal(args, &v); err != nil {
				return Result{}, err
			}
			idf, ok := v["task_id"].(float64)
			if !ok {
				return Result{}, errors.New("task_id is required")
			}
			id := int64(idf)
			body := str(v, "body")
			if body == "" {
				return Result{}, errors.New("body is required")
			}
			t, err := taskForMember(ctx, d, id)
			if err != nil {
				return Result{}, err
			}
			if _, err := d.Store.AddComment(ctx, id, d.ActorID, body); err != nil {
				return Result{}, err
			}
			publishEvent(d, events.Event{Type: "task_comment", ProjectID: t.ProjectID, TaskID: id, Title: t.Title, Actor: d.Actor})
			return Result{Ok: true, Message: fmt.Sprintf("comment added to task %d", id)}, nil
		},
	},
	{
		Name:        "set_assignee",
		Description: "Assign a task to a project member by email, or clear it with an empty email",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "integer"},
				"email":   map[string]any{"type": "string"},
			},
			"required": []string{"task_id", "email"},
		},
		Handler: func(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
			var v map[string]any
			if err := json.Unmarshal(args, &v); err != nil {
				return Result{}, err
			}
			idf, ok := v["task_id"].(float64)
			if !ok {
				return Result{}, errors.New("task_id is required")
			}
			id := int64(idf)
			task, err := taskForMember(ctx, d, id)
			if err != nil {
				return Result{}, err
			}
			email, ok := v["email"].(string)
			if !ok {
				return Result{}, errors.New("email is required")
			}
			if email == "" {
				if err := d.Store.UpdateTask(ctx, id, []string{"assignee_id"}, []any{nil}); err != nil {
					return Result{}, err
				}
				publishEvent(d, events.Event{Type: "task_updated", ProjectID: task.ProjectID, TaskID: id, Title: task.Title, Actor: d.Actor})
				return Result{Ok: true, Message: fmt.Sprintf("assignee cleared for task %d", id)}, nil
			}
			u, err := d.Store.UserByEmail(ctx, email)
			if err != nil {
				return Result{}, err
			}
			if u == nil {
				return Result{}, errors.New("assignee not found")
			}
			if err := d.Store.ValidateAssignee(ctx, task.ProjectID, u.ID); err != nil {
				return Result{}, err
			}
			if err := d.Store.UpdateTask(ctx, id, []string{"assignee_id"}, []any{u.ID}); err != nil {
				return Result{}, err
			}
			publishEvent(d, events.Event{Type: "task_updated", ProjectID: task.ProjectID, TaskID: id, Title: task.Title, Actor: d.Actor})
			return Result{Ok: true, Message: fmt.Sprintf("task %d assigned to %s", id, u.Email)}, nil
		},
	},
	{
		Name:        "list_epics",
		Description: "List epics (task groups) in a project space",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project": map[string]any{"type": "string", "description": "Project space slug"},
			},
			"required": []string{"project"},
		},
		Handler: func(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
			var v map[string]any
			if err := json.Unmarshal(args, &v); err != nil {
				return Result{}, err
			}
			p, err := projectBySlug(ctx, d, str(v, "project"))
			if err != nil {
				return Result{}, err
			}
			epics, err := d.Store.EpicsOfProject(ctx, p.ID)
			if err != nil {
				return Result{}, err
			}
			return Result{Ok: true, Data: epics}, nil
		},
	},
	{
		Name:        "create_epic",
		Description: "Create an epic (a group of tasks) in a project space",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project":     map[string]any{"type": "string"},
				"title":       map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
			},
			"required": []string{"project", "title"},
		},
		Handler: func(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
			var v map[string]any
			if err := json.Unmarshal(args, &v); err != nil {
				return Result{}, err
			}
			p, err := projectBySlug(ctx, d, str(v, "project"))
			if err != nil {
				return Result{}, err
			}
			title := str(v, "title")
			if title == "" {
				return Result{}, errors.New("title is required")
			}
			id, err := d.Store.CreateEpic(ctx, p.ID, title, str(v, "description"), domain.StatusBacklog)
			if err != nil {
				return Result{}, err
			}
			eid := id
			if err := d.Store.Audit(ctx, d.Actor, "create_epic", "epic", &eid, map[string]any{"title": title, "project": p.Slug}); err != nil {
				return Result{}, err
			}
			return Result{Ok: true, Message: fmt.Sprintf("epic %d created", id), Data: map[string]any{"id": id}}, nil
		},
	},
	{
		Name:        "set_task_epic",
		Description: "Attach a task to an epic, or detach it when epic_id is null",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "integer"},
				"epic_id": map[string]any{"type": "integer", "description": "Epic id, omit or null to detach"},
			},
			"required": []string{"task_id"},
		},
		Handler: func(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
			var v map[string]any
			if err := json.Unmarshal(args, &v); err != nil {
				return Result{}, err
			}
			idf, ok := v["task_id"].(float64)
			if !ok {
				return Result{}, errors.New("task_id is required")
			}
			id := int64(idf)
			t, err := d.Store.TaskByID(ctx, id)
			if err != nil {
				return Result{}, err
			}
			if t == nil {
				return Result{}, fmt.Errorf("task %d not found", id)
			}
			member, err := d.Store.IsMember(ctx, t.ProjectID, d.ActorID)
			if err != nil {
				return Result{}, err
			}
			if !member {
				return Result{}, fmt.Errorf("access denied to task %d", id)
			}
			var epicID any
			if raw, ok := v["epic_id"]; ok && raw != nil {
				ef, ok := raw.(float64)
				if !ok {
					return Result{}, errors.New("bad epic_id")
				}
				epic, err := d.Store.EpicByID(ctx, int64(ef))
				if err != nil {
					return Result{}, err
				}
				if epic == nil || epic.ProjectID != t.ProjectID {
					return Result{}, fmt.Errorf("epic %d not found in task's project", int64(ef))
				}
				epicID = int64(ef)
			}
			if err := d.Store.UpdateTask(ctx, id, []string{"epic_id"}, []any{epicID}); err != nil {
				return Result{}, err
			}
			return Result{Ok: true, Message: fmt.Sprintf("task %d epic updated", id)}, nil
		},
	},
	{
		Name:        "add_dependency",
		Description: "Make a task depend on another task (task_id cannot be moved to done until depends_on is done)",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":    map[string]any{"type": "integer"},
				"depends_on": map[string]any{"type": "integer", "description": "Id of the task that must be done first"},
			},
			"required": []string{"task_id", "depends_on"},
		},
		Handler: func(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
			var v map[string]any
			if err := json.Unmarshal(args, &v); err != nil {
				return Result{}, err
			}
			idf, ok := v["task_id"].(float64)
			if !ok {
				return Result{}, errors.New("task_id is required")
			}
			id := int64(idf)
			df, ok := v["depends_on"].(float64)
			if !ok {
				return Result{}, errors.New("depends_on is required")
			}
			depID := int64(df)
			t, err := d.Store.TaskByID(ctx, id)
			if err != nil {
				return Result{}, err
			}
			if t == nil {
				return Result{}, fmt.Errorf("task %d not found", id)
			}
			member, err := d.Store.IsMember(ctx, t.ProjectID, d.ActorID)
			if err != nil {
				return Result{}, err
			}
			if !member {
				return Result{}, fmt.Errorf("access denied to task %d", id)
			}
			if depID == id {
				return Result{}, errors.New("task cannot depend on itself")
			}
			dep, err := d.Store.TaskByID(ctx, depID)
			if err != nil {
				return Result{}, err
			}
			if dep == nil {
				return Result{}, fmt.Errorf("dependency task %d not found", depID)
			}
			if dep.ProjectID != t.ProjectID {
				return Result{}, errors.New("dependency must be in the same project")
			}
			cycle, err := d.Store.WouldCreateCycle(ctx, id, depID)
			if err != nil {
				return Result{}, err
			}
			if cycle {
				return Result{}, errors.New("dependency would create a cycle")
			}
			if err := d.Store.AddDependency(ctx, id, depID); err != nil {
				return Result{}, err
			}
			eid := id
			if err := d.Store.Audit(ctx, d.Actor, "add_dependency", "task", &eid, map[string]any{"depends_on": depID}); err != nil {
				return Result{}, err
			}
			publishEvent(d, events.Event{Type: "dependency_added", ProjectID: t.ProjectID, TaskID: id, Title: t.Title, Actor: d.Actor})
			return Result{Ok: true, Message: fmt.Sprintf("task %d now depends on task %d", id, depID)}, nil
		},
	},
	{
		Name:        "remove_dependency",
		Description: "Remove a dependency between two tasks",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":    map[string]any{"type": "integer"},
				"depends_on": map[string]any{"type": "integer", "description": "Id of the dependency to remove"},
			},
			"required": []string{"task_id", "depends_on"},
		},
		Handler: func(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
			var v map[string]any
			if err := json.Unmarshal(args, &v); err != nil {
				return Result{}, err
			}
			idf, ok := v["task_id"].(float64)
			if !ok {
				return Result{}, errors.New("task_id is required")
			}
			id := int64(idf)
			df, ok := v["depends_on"].(float64)
			if !ok {
				return Result{}, errors.New("depends_on is required")
			}
			depID := int64(df)
			t, err := d.Store.TaskByID(ctx, id)
			if err != nil {
				return Result{}, err
			}
			if t == nil {
				return Result{}, fmt.Errorf("task %d not found", id)
			}
			member, err := d.Store.IsMember(ctx, t.ProjectID, d.ActorID)
			if err != nil {
				return Result{}, err
			}
			if !member {
				return Result{}, fmt.Errorf("access denied to task %d", id)
			}
			if err := d.Store.RemoveDependency(ctx, id, depID); err != nil {
				return Result{}, err
			}
			eid := id
			if err := d.Store.Audit(ctx, d.Actor, "remove_dependency", "task", &eid, map[string]any{"depends_on": depID}); err != nil {
				return Result{}, err
			}
			publishEvent(d, events.Event{Type: "dependency_removed", ProjectID: t.ProjectID, TaskID: id, Title: t.Title, Actor: d.Actor})
			return Result{Ok: true, Message: fmt.Sprintf("task %d no longer depends on task %d", id, depID)}, nil
		},
	},
	{
		Name:        "set_task_recurring",
		Description: "Set task recurrence (daily, weekly, monthly) or clear it with empty string",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":   map[string]any{"type": "integer"},
				"recurring": map[string]any{"type": "string", "enum": domain.RecurringSpecs, "description": "Omit or empty to clear"},
			},
			"required": []string{"task_id"},
		},
		Handler: func(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
			var v map[string]any
			if err := json.Unmarshal(args, &v); err != nil {
				return Result{}, err
			}
			idf, ok := v["task_id"].(float64)
			if !ok {
				return Result{}, errors.New("task_id is required")
			}
			id := int64(idf)
			t, err := d.Store.TaskByID(ctx, id)
			if err != nil {
				return Result{}, err
			}
			if t == nil {
				return Result{}, fmt.Errorf("task %d not found", id)
			}
			member, err := d.Store.IsMember(ctx, t.ProjectID, d.ActorID)
			if err != nil {
				return Result{}, err
			}
			if !member {
				return Result{}, fmt.Errorf("access denied to task %d", id)
			}
			recurring := str(v, "recurring")
			if recurring != "" && !validRecurring(recurring) {
				return Result{}, fmt.Errorf("unknown recurring %q", recurring)
			}
			if err := d.Store.UpdateTask(ctx, id, []string{"recurring"}, []any{recurring}); err != nil {
				return Result{}, err
			}
			publishEvent(d, events.Event{Type: "task_updated", ProjectID: t.ProjectID, TaskID: id, Title: t.Title, Actor: d.Actor})
			return Result{Ok: true, Message: fmt.Sprintf("task %d recurring set to %q", id, recurring)}, nil
		},
	},
	{
		Name:        "delete_task",
		Description: "Delete a task by id (comments, labels, attachments and dependency links are removed too). Refused while other tasks depend on it",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "integer"},
			},
			"required": []string{"task_id"},
		},
		Handler: func(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
			var v map[string]any
			if err := json.Unmarshal(args, &v); err != nil {
				return Result{}, err
			}
			idf, ok := v["task_id"].(float64)
			if !ok {
				return Result{}, errors.New("task_id is required")
			}
			id := int64(idf)
			t, err := d.Store.TaskByID(ctx, id)
			if err != nil {
				return Result{}, err
			}
			if t == nil {
				return Result{}, fmt.Errorf("task %d not found", id)
			}
			member, err := d.Store.IsMember(ctx, t.ProjectID, d.ActorID)
			if err != nil {
				return Result{}, err
			}
			if !member {
				return Result{}, fmt.Errorf("access denied to task %d", id)
			}
			dependents, err := d.Store.DependentsDetail(ctx, id)
			if err != nil {
				return Result{}, err
			}
			if len(dependents) > 0 {
				titles := make([]string, 0, len(dependents))
				for _, dep := range dependents {
					titles = append(titles, fmt.Sprintf("#%d %s", dep.ID, dep.Title))
				}
				return Result{}, fmt.Errorf("task %d blocks other tasks: %s", id, strings.Join(titles, ", "))
			}
			if err := d.Store.DeleteTask(ctx, id); err != nil {
				return Result{}, err
			}
			eid := id
			if err := d.Store.Audit(ctx, d.Actor, "delete_task", "task", &eid, nil); err != nil {
				return Result{}, err
			}
			publishEvent(d, events.Event{Type: "task_deleted", ProjectID: t.ProjectID, TaskID: id, Title: t.Title, Actor: d.Actor})
			return Result{Ok: true, Message: fmt.Sprintf("task %d deleted", id)}, nil
		},
	},
}

func Names() []string {
	out := make([]string, len(Registry))
	for i, t := range Registry {
		out[i] = t.Name
	}
	return out
}

func ByName(name string) *Tool {
	for i := range Registry {
		if Registry[i].Name == name {
			return &Registry[i]
		}
	}
	return nil
}
