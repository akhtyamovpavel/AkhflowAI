package domain

import "time"

type User struct {
	ID          int64     `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
	Role        string    `json:"role,omitempty"`
}

type Project struct {
	ID      int64  `json:"id"`
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	OwnerID int64  `json:"owner_id"`
}

const (
	StatusBacklog    = "backlog"
	StatusTodo       = "todo"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
)

var Statuses = []string{StatusBacklog, StatusTodo, StatusInProgress, StatusDone}

const (
	PriorityLow    = "low"
	PriorityMedium = "medium"
	PriorityHigh   = "high"
)

var Priorities = []string{PriorityLow, PriorityMedium, PriorityHigh}

var RecurringSpecs = []string{"daily", "weekly", "monthly"}

func ValidRecurring(s string) bool {
	if s == "" {
		return true
	}
	for _, v := range RecurringSpecs {
		if v == s {
			return true
		}
	}
	return false
}

// RecurrenceStep returns a function advancing a date by one recurrence interval.
func RecurrenceStep(spec string) (func(time.Time) time.Time, bool) {
	switch spec {
	case "daily":
		return func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }, true
	case "weekly":
		return func(t time.Time) time.Time { return t.AddDate(0, 0, 7) }, true
	case "monthly":
		return func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }, true
	}
	return nil, false
}

type Task struct {
	ID          int64       `json:"id"`
	ProjectID   int64       `json:"project_id"`
	ParentID    *int64      `json:"parent_id,omitempty"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Status      string      `json:"status"`
	Priority    string      `json:"priority"`
	EstimateMin *int        `json:"estimate_min,omitempty"`
	DueDate     *time.Time  `json:"due_date,omitempty"`
	AuthorID    int64       `json:"author_id"`
	AssigneeID  *int64      `json:"assignee_id,omitempty"`
	Recurring   string      `json:"recurring,omitempty"`
	EpicID      *int64      `json:"epic_id,omitempty"`
	Labels      []string    `json:"labels,omitempty"`
	DependsOn   []int64     `json:"depends_on,omitempty"`
	Blocks      []int64     `json:"blocks,omitempty"`
	Attachment  *Attachment `json:"attachment,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

type Comment struct {
	ID        int64     `json:"id"`
	TaskID    int64     `json:"task_id"`
	AuthorID  int64     `json:"author_id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type AuditEntry struct {
	ID        int64          `json:"id"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	Entity    string         `json:"entity"`
	EntityID  *int64         `json:"entity_id,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type Chat struct {
	ID        int64     `json:"id"`
	ProjectID int64     `json:"project_id"`
	UserID    int64     `json:"user_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

type Attachment struct {
	ID         int64     `json:"id"`
	TaskID     int64     `json:"task_id"`
	Filename   string    `json:"filename"`
	SizeBytes  int64     `json:"size_bytes"`
	StorageKey string    `json:"storage_key"`
	CreatedAt  time.Time `json:"created_at"`
}

type AgentMessage struct {
	ID        int64     `json:"id"`
	ChatID    int64     `json:"chat_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	ToolCalls any       `json:"tool_calls,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Epic struct {
	ID          int64     `json:"id"`
	ProjectID   int64     `json:"project_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}
