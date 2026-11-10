package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"akhflow.ai/akhflow/internal/domain"
	"akhflow.ai/akhflow/internal/events"
	"akhflow.ai/akhflow/internal/llm"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/toolbox"
)

type Server struct {
	Auth  *Auth
	Store *store.Store
	LLM   *llm.Client
	Bus   *events.Bus
}

// maxChatHistory caps how many previous turns are fed into the agent context.
const maxChatHistory = 20

func New(st *store.Store, auth *Auth, llmClient *llm.Client, bus *events.Bus) *Server {
	return &Server{Auth: auth, Store: st, LLM: llmClient, Bus: bus}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("POST /api/auth/register", s.register)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.Handle("GET /api/projects", s.Auth.Middleware(http.HandlerFunc(s.listProjects)))
	mux.Handle("POST /api/projects", s.Auth.Middleware(http.HandlerFunc(s.createProject)))
	mux.Handle("GET /api/projects/{slug}/tasks", s.Auth.Middleware(http.HandlerFunc(s.listTasks)))
	mux.Handle("GET /api/projects/{slug}/members", s.Auth.Middleware(http.HandlerFunc(s.listMembers)))
	mux.Handle("POST /api/projects/{slug}/tasks", s.Auth.Middleware(http.HandlerFunc(s.createTask)))
	mux.Handle("GET /api/projects/{slug}/chats", s.Auth.Middleware(http.HandlerFunc(s.listChats)))
	mux.Handle("POST /api/projects/{slug}/chats", s.Auth.Middleware(http.HandlerFunc(s.createChat)))
	mux.Handle("GET /api/chats/{id}/messages", s.Auth.Middleware(http.HandlerFunc(s.listMessages)))
	mux.Handle("POST /api/chats/{id}/messages", s.Auth.Middleware(http.HandlerFunc(s.sendMessage)))
	mux.Handle("GET /api/projects/{slug}/epics", s.Auth.Middleware(http.HandlerFunc(s.listEpics)))
	mux.Handle("POST /api/projects/{slug}/epics", s.Auth.Middleware(http.HandlerFunc(s.createEpic)))
	mux.Handle("GET /api/epics/{id}", s.Auth.Middleware(http.HandlerFunc(s.getEpic)))
	mux.Handle("DELETE /api/epics/{id}", s.Auth.Middleware(http.HandlerFunc(s.deleteEpic)))
	mux.Handle("GET /api/tasks/{id}", s.Auth.Middleware(http.HandlerFunc(s.getTask)))
	mux.Handle("PATCH /api/tasks/{id}", s.Auth.Middleware(http.HandlerFunc(s.patchTask)))
	mux.Handle("DELETE /api/tasks/{id}", s.Auth.Middleware(http.HandlerFunc(s.deleteTask)))
	mux.Handle("GET /api/tasks/{id}/comments", s.Auth.Middleware(http.HandlerFunc(s.listComments)))
	mux.Handle("POST /api/tasks/{id}/comments", s.Auth.Middleware(http.HandlerFunc(s.addComment)))
	mux.Handle("GET /api/tasks/{id}/dependencies", s.Auth.Middleware(http.HandlerFunc(s.listDependencies)))
	mux.Handle("POST /api/tasks/{id}/dependencies", s.Auth.Middleware(http.HandlerFunc(s.addDependency)))
	mux.Handle("DELETE /api/tasks/{id}/dependencies/{dep}", s.Auth.Middleware(http.HandlerFunc(s.deleteDependency)))
	mux.Handle("GET /api/projects/{slug}/events", s.Auth.Middleware(http.HandlerFunc(s.sseEvents)))
	mux.Handle("POST /api/agent/chat", s.Auth.Middleware(http.HandlerFunc(s.agentChat)))
	return mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "akhflow"})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !strings.Contains(req.Email, "@") || len(req.Password) < 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid email or password"})
		return
	}
	if req.DisplayName == "" {
		req.DisplayName = req.Email
	}
	existing, err := s.Store.UserByEmail(r.Context(), req.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if existing != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "user exists"})
		return
	}
	id, err := s.Store.CreateUser(r.Context(), req.Email, req.DisplayName, s.Auth.HashPassword(req.Password))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	token, err := s.Auth.CreateToken(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "user": map[string]any{"id": id, "email": req.Email, "display_name": req.DisplayName}})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	hash, err := s.Store.PasswordHashByEmail(r.Context(), req.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if hash == "" || !s.Auth.VerifyPassword(req.Password, hash) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad credentials"})
		return
	}
	u, err := s.Store.UserByEmail(r.Context(), req.Email)
	if err != nil || u == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "user lookup failed"})
		return
	}
	token, err := s.Auth.CreateToken(u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": u})
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	id, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	projects, err := s.Store.ProjectsOfUser(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	id, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	var req struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	slug := req.Slug
	if slug == "" {
		slug = strings.ToLower(strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				return r
			case r == ' ', r == '-', r == '_':
				return '-'
			}
			return -1
		}, strings.ToLower(req.Name)))
	}
	if slug == "" || slug == "-" {
		slug = fmt.Sprintf("project-%d", time.Now().UnixNano())
	}
	pid, err := s.Store.CreateProject(r.Context(), slug, req.Name, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": pid, "slug": slug, "name": req.Name})
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	id, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	slug := r.PathValue("slug")
	p, err := s.Store.ProjectBySlug(r.Context(), slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	member, err := s.Store.IsMember(r.Context(), p.ID, id)
	if err != nil || !member {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}
	tasks, err := s.Store.ListTasks(r.Context(), p.ID, r.URL.Query().Get("status"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	id, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	slug := r.PathValue("slug")
	p, err := s.Store.ProjectBySlug(r.Context(), slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	member, err := s.Store.IsMember(r.Context(), p.ID, id)
	if err != nil || !member {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}
	var req struct {
		Title       string     `json:"title"`
		Description string     `json:"description"`
		Status      string     `json:"status"`
		Priority    string     `json:"priority"`
		EstimateMin *int       `json:"estimate_min"`
		DueDate     *time.Time `json:"due_date"`
		AssigneeID  *int64     `json:"assignee_id"`
		Labels      []string   `json:"labels"`
		Recurring   string     `json:"recurring"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title required"})
		return
	}
	status := req.Status
	if status == "" {
		status = domain.StatusTodo
	}
	priority := req.Priority
	if priority == "" {
		priority = domain.PriorityMedium
	}
	t := domain.Task{
		ProjectID:   p.ID,
		Title:       req.Title,
		Description: req.Description,
		Status:      status,
		Priority:    priority,
		EstimateMin: req.EstimateMin,
		DueDate:     req.DueDate,
		AuthorID:    id,
		AssigneeID:  req.AssigneeID,
		Labels:      req.Labels,
	}
	if req.Recurring != "" {
		if !domain.ValidRecurring(req.Recurring) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad recurring"})
			return
		}
		t.Recurring = req.Recurring
	}
	tid, err := s.Store.CreateTask(r.Context(), t)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	eid := tid
	if err := s.Store.Audit(r.Context(), s.Auth.ActorName(r.Context()), "create_task", "task", &eid, map[string]any{"title": req.Title}); err != nil {
		log.Printf("audit: %v", err)
	}
	if s.Bus != nil {
		s.Bus.Publish(events.Event{Type: "task_created", ProjectID: p.ID, TaskID: tid, Title: req.Title, Actor: s.Auth.ActorName(r.Context())})
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": tid})
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	t, err := s.Store.TaskByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if t == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	comments, err := s.Store.ListComments(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	deps, err := s.Store.DependenciesDetail(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	blocks, err := s.Store.DependentsDetail(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	history, err := s.Store.AuditForTask(r.Context(), id, 50)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": t, "comments": comments, "dependencies": deps, "blocks": blocks, "history": history})
}

func (s *Server) patchTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	t, err := s.Store.TaskByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if t == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	var body map[string]any
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var sets []string
	var args []any
	if v, ok := body["title"]; ok {
		s, ok := v.(string)
		if !ok || s == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad title"})
			return
		}
		sets = append(sets, "title")
		args = append(args, s)
	}
	if v, ok := body["description"]; ok {
		s, _ := v.(string)
		sets = append(sets, "description")
		args = append(args, s)
	}
	if v, ok := body["status"]; ok {
		st, _ := v.(string)
		if !validStatus(st) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad status"})
			return
		}
		if st == domain.StatusDone {
			blocked, err := s.Store.UnfinishedDependencies(r.Context(), id)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			if len(blocked) > 0 {
				titles := make([]string, 0, len(blocked))
				for _, b := range blocked {
					titles = append(titles, fmt.Sprintf("#%d %s", b.ID, b.Title))
				}
				writeJSON(w, http.StatusConflict, map[string]any{
					"error":      "task has unfinished dependencies",
					"depends_on": blocked,
					"detail":     fmt.Sprintf("завершите сначала: %s", strings.Join(titles, ", ")),
				})
				return
			}
		}
		sets = append(sets, "status")
		args = append(args, st)
	}
	if v, ok := body["priority"]; ok {
		s, _ := v.(string)
		if !validPriority(s) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad priority"})
			return
		}
		sets = append(sets, "priority")
		args = append(args, s)
	}
	if v, ok := body["estimate_min"]; ok {
		if v == nil {
			sets = append(sets, "estimate_min")
			args = append(args, nil)
		} else {
			f, ok := v.(float64)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad estimate_min"})
				return
			}
			n := int(f)
			sets = append(sets, "estimate_min")
			args = append(args, n)
		}
	}
	if v, ok := body["due_date"]; ok {
		if v == nil {
			sets = append(sets, "due_date")
			args = append(args, nil)
		} else {
			s, ok := v.(string)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad due_date"})
				return
			}
			dt, err := time.Parse(time.DateOnly, s)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad due_date format"})
				return
			}
			sets = append(sets, "due_date")
			args = append(args, dt)
		}
	}
	if v, ok := body["assignee_id"]; ok {
		if v == nil {
			sets = append(sets, "assignee_id")
			args = append(args, nil)
		} else {
			f, ok := v.(float64)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad assignee_id"})
				return
			}
			n := int64(f)
			sets = append(sets, "assignee_id")
			args = append(args, n)
		}
	}
	if v, ok := body["recurring"]; ok {
		rc, _ := v.(string)
		if rc != "" && !domain.ValidRecurring(rc) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad recurring"})
			return
		}
		sets = append(sets, "recurring")
		args = append(args, rc)
	}
	if v, ok := body["epic_id"]; ok {
		if v == nil {
			sets = append(sets, "epic_id")
			args = append(args, nil)
		} else {
			f, ok := v.(float64)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad epic_id"})
				return
			}
			n := int64(f)
			epic, err := s.Store.EpicByID(r.Context(), n)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			if epic == nil || epic.ProjectID != t.ProjectID {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "epic not found in this project"})
				return
			}
			sets = append(sets, "epic_id")
			args = append(args, n)
		}
	}
	if err := s.Store.UpdateTask(r.Context(), id, sets, args); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// if status moved to done and task has recurring, spawn next occurrence
	if statusToDone(sets, args, t.Status) && t.Recurring != "" {
		if err := s.spawnNextRecurring(r.Context(), id, t); err != nil {
			log.Printf("recurring spawn failed for task %d: %v", id, err)
		}
	}
	eid := id
	if err := s.Store.Audit(r.Context(), s.Auth.ActorName(r.Context()), "update_task", "task", &eid, body); err != nil {
		log.Printf("audit: %v", err)
	}
	// publish events
	if s.Bus != nil {
		for _, set := range sets {
			if set == "status" {
				s.Bus.Publish(events.Event{Type: "task_moved", ProjectID: t.ProjectID, TaskID: id, Title: t.Title, Status: extractStatus(sets, args), Actor: s.Auth.ActorName(r.Context())})
			} else if set == "recurring" {
				s.Bus.Publish(events.Event{Type: "task_updated", ProjectID: t.ProjectID, TaskID: id, Title: t.Title, Actor: s.Auth.ActorName(r.Context())})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// deleteTask removes a task with all its comments, labels, attachments and
// dependency links. Refuses with 409 while other tasks depend on it.
func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	uid, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	t, err := s.Store.TaskByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if t == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	member, err := s.Store.IsMember(r.Context(), t.ProjectID, uid)
	if err != nil || !member {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}
	dependents, err := s.Store.DependentsDetail(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if len(dependents) > 0 {
		titles := make([]string, 0, len(dependents))
		for _, d := range dependents {
			titles = append(titles, fmt.Sprintf("#%d %s", d.ID, d.Title))
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":  "task blocks other tasks",
			"blocks": dependents,
			"detail": fmt.Sprintf("сначала уберите зависимости: %s", strings.Join(titles, ", ")),
		})
		return
	}
	if err := s.Store.DeleteTask(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	eid := id
	if err := s.Store.Audit(r.Context(), s.Auth.ActorName(r.Context()), "delete_task", "task", &eid, nil); err != nil {
		log.Printf("audit: %v", err)
	}
	if s.Bus != nil {
		s.Bus.Publish(events.Event{Type: "task_deleted", ProjectID: t.ProjectID, TaskID: id, Title: t.Title, Actor: s.Auth.ActorName(r.Context())})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// statusToDone checks if the update sets status to done.
func statusToDone(sets []string, args []any, oldStatus string) bool {
	if oldStatus == domain.StatusDone {
		return false
	}
	for i, s := range sets {
		if s == "status" {
			if i < len(args) {
				if st, ok := args[i].(string); ok {
					return st == domain.StatusDone
				}
			}
		}
	}
	return false
}

// extractStatus returns the new status value from the update sets/args.
func extractStatus(sets []string, args []any) string {
	for i, s := range sets {
		if s == "status" && i < len(args) {
			if st, ok := args[i].(string); ok {
				return st
			}
		}
	}
	return ""
}

// spawnNextRecurring spawns the next occurrence of a recurring task.
func (s *Server) spawnNextRecurring(ctx context.Context, taskID int64, orig *domain.Task) error {
	d := toolbox.Deps{
		Store:   s.Store,
		ActorID: 0, // system, no user
		Actor:   "scheduler",
		Bus:     s.Bus,
	}
	_, err := toolbox.SpawnNextOccurrence(ctx, d, orig, true)
	return err
}
func (s *Server) listComments(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	comments, err := s.Store.ListComments(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, comments)
}

func (s *Server) addComment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	uid, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	if err := readJSON(r, &req); err != nil || req.Body == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body required"})
		return
	}
	cid, err := s.Store.AddComment(r.Context(), id, uid, req.Body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if s.Bus != nil {
		s.Bus.Publish(events.Event{Type: "task_comment", ProjectID: 0, TaskID: id, Title: "", Actor: s.Auth.ActorName(r.Context())})
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": cid})
}

func (s *Server) listDependencies(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	uid, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	t, err := s.Store.TaskByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if t == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	member, err := s.Store.IsMember(r.Context(), t.ProjectID, uid)
	if err != nil || !member {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}
	deps, err := s.Store.DependenciesDetail(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	blocks, err := s.Store.DependentsDetail(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dependencies": deps, "blocks": blocks})
}

func (s *Server) addDependency(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	uid, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	t, err := s.Store.TaskByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if t == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	member, err := s.Store.IsMember(r.Context(), t.ProjectID, uid)
	if err != nil || !member {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}
	var req struct {
		DependsOn *int64 `json:"depends_on"`
	}
	if err := readJSON(r, &req); err != nil || req.DependsOn == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "depends_on required"})
		return
	}
	depID := *req.DependsOn
	if depID == id {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "task cannot depend on itself"})
		return
	}
	dep, err := s.Store.TaskByID(r.Context(), depID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if dep == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "dependency task not found"})
		return
	}
	if dep.ProjectID != t.ProjectID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "dependency must be in the same project"})
		return
	}
	cycle, err := s.Store.WouldCreateCycle(r.Context(), id, depID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if cycle {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "dependency would create a cycle"})
		return
	}
	if err := s.Store.AddDependency(r.Context(), id, depID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	eid := id
	if err := s.Store.Audit(r.Context(), s.Auth.ActorName(r.Context()), "add_dependency", "task", &eid, map[string]any{"depends_on": depID}); err != nil {
		log.Printf("audit: %v", err)
	}
	if s.Bus != nil {
		s.Bus.Publish(events.Event{Type: "dependency_added", ProjectID: t.ProjectID, TaskID: id, Title: t.Title, Actor: s.Auth.ActorName(r.Context())})
	}
	writeJSON(w, http.StatusCreated, map[string]any{"task_id": id, "depends_on": depID})
}

func (s *Server) deleteDependency(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	depID, err := strconv.ParseInt(r.PathValue("dep"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad dep"})
		return
	}
	uid, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	t, err := s.Store.TaskByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if t == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	member, err := s.Store.IsMember(r.Context(), t.ProjectID, uid)
	if err != nil || !member {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}
	if err := s.Store.RemoveDependency(r.Context(), id, depID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	eid := id
	if err := s.Store.Audit(r.Context(), s.Auth.ActorName(r.Context()), "remove_dependency", "task", &eid, map[string]any{"depends_on": depID}); err != nil {
		log.Printf("audit: %v", err)
	}
	if s.Bus != nil {
		s.Bus.Publish(events.Event{Type: "dependency_removed", ProjectID: t.ProjectID, TaskID: id, Title: t.Title, Actor: s.Auth.ActorName(r.Context())})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) sseEvents(w http.ResponseWriter, r *http.Request) {
	// Auth: accept Bearer header OR ?token= query (EventSource cannot set headers)
	tokenStr := r.URL.Query().Get("token")
	var uid int64
	if tokenStr != "" {
		id, err := s.Auth.ParseToken(tokenStr)
		if err == nil {
			uid = id
		}
	}
	if uid == 0 {
		h := r.Header.Get("Authorization")
		if strings.HasPrefix(h, "Bearer ") {
			if id, err := s.Auth.ParseToken(strings.TrimPrefix(h, "Bearer ")); err == nil {
				uid = id
			}
		}
	}
	if uid == 0 {
		http.Error(w, `{"error":"missing token"}`, http.StatusUnauthorized)
		return
	}
	u, err := s.Store.UserByID(r.Context(), uid)
	if err != nil || u == nil {
		http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
		return
	}
	p, err := s.Store.ProjectBySlug(r.Context(), r.PathValue("slug"))
	if err != nil || p == nil {
		http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
		return
	}
	member, err := s.Store.IsMember(r.Context(), p.ID, uid)
	if err != nil || !member {
		http.Error(w, `{"error":"access denied"}`, http.StatusForbidden)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	sub := s.Bus.Subscribe(p.ID)
	defer s.Bus.Unsubscribe(sub)
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case e := <-sub.Ch:
			payload, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

func (s *Server) agentChat(w http.ResponseWriter, r *http.Request) {
	uid, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := readJSON(r, &req); err != nil || req.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message required"})
		return
	}
	u, err := s.Store.UserByID(r.Context(), uid)
	if err != nil || u == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "user lookup failed"})
		return
	}
	agent := llm.Agent{
		Client: s.LLM,
		Deps: toolbox.Deps{
			Store:   s.Store,
			ActorID: uid,
			Actor:   u.Email,
		},
	}
	result, err := agent.Run(r.Context(), req.Message, "", nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) listChats(w http.ResponseWriter, r *http.Request) {
	uid, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	p, ok := s.projectBySlug(w, r, uid)
	if !ok {
		return
	}
	chats, err := s.Store.ChatsOfProject(r.Context(), p.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, chats)
}

func (s *Server) createChat(w http.ResponseWriter, r *http.Request) {
	uid, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	p, ok := s.projectBySlug(w, r, uid)
	if !ok {
		return
	}
	id, err := s.Store.CreateChat(r.Context(), p.ID, uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	uid, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	chat, err := s.Store.ChatByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if chat == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "chat not found"})
		return
	}
	member, err := s.Store.IsMember(r.Context(), chat.ProjectID, uid)
	if err != nil || !member {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}
	messages, err := s.Store.MessagesOfChat(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, messages)
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	uid, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	chat, err := s.Store.ChatByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if chat == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "chat not found"})
		return
	}
	member, err := s.Store.IsMember(r.Context(), chat.ProjectID, uid)
	if err != nil || !member {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := readJSON(r, &req); err != nil || req.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message required"})
		return
	}
	// load history BEFORE appending the new message so the current prompt
	// is not duplicated in the context
	historyMsgs, err := s.Store.MessagesOfChat(r.Context(), chat.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	history := make([]llm.Message, 0, len(historyMsgs))
	for _, m := range historyMsgs {
		role := "user"
		if m.Role == "agent" {
			role = "assistant"
		}
		history = append(history, llm.Message{Role: role, Content: m.Content})
	}
	if len(history) > maxChatHistory {
		history = history[len(history)-maxChatHistory:]
	}
	if _, err := s.Store.AddAgentMessage(r.Context(), chat.ID, "user", req.Message, nil); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if chat.Title == "" {
		s.Store.SetChatTitle(r.Context(), chat.ID, truncate(req.Message, 60))
	}
	p, err := s.Store.ProjectByID(r.Context(), chat.ProjectID)
	if err != nil || p == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "project lookup failed"})
		return
	}
	u, err := s.Store.UserByID(r.Context(), uid)
	if err != nil || u == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "user lookup failed"})
		return
	}
	agent := llm.Agent{
		Client: s.LLM,
		Deps: toolbox.Deps{
			Store:   s.Store,
			ActorID: uid,
			Actor:   u.Email,
		},
	}
	result, err := agent.Run(r.Context(), req.Message, p.Slug, history)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if _, err := s.Store.AddAgentMessage(r.Context(), chat.ID, "agent", result.Reply, result.ToolCalls); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) projectBySlug(w http.ResponseWriter, r *http.Request, uid int64) (*domain.Project, bool) {
	p, err := s.Store.ProjectBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return nil, false
	}
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return nil, false
	}
	member, err := s.Store.IsMember(r.Context(), p.ID, uid)
	if err != nil || !member {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return nil, false
	}
	return p, true
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) {
	uid, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	p, ok := s.projectBySlug(w, r, uid)
	if !ok {
		return
	}
	members, err := s.Store.ListProjectMembers(r.Context(), p.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, members)
}

func (s *Server) listEpics(w http.ResponseWriter, r *http.Request) {
	uid, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	p, ok := s.projectBySlug(w, r, uid)
	if !ok {
		return
	}
	epics, err := s.Store.EpicsOfProject(r.Context(), p.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, epics)
}

func (s *Server) createEpic(w http.ResponseWriter, r *http.Request) {
	uid, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	p, ok := s.projectBySlug(w, r, uid)
	if !ok {
		return
	}
	var req struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Status      string `json:"status"`
	}
	if err := readJSON(r, &req); err != nil || req.Title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title required"})
		return
	}
	status := req.Status
	if status == "" {
		status = domain.StatusBacklog
	}
	if !validStatus(status) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad status"})
		return
	}
	id, err := s.Store.CreateEpic(r.Context(), p.ID, req.Title, req.Description, status)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) getEpic(w http.ResponseWriter, r *http.Request) {
	uid, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	epic, err := s.Store.EpicByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if epic == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "epic not found"})
		return
	}
	member, err := s.Store.IsMember(r.Context(), epic.ProjectID, uid)
	if err != nil || !member {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}
	tasks, err := s.Store.ListTasksByEpic(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"epic": epic, "tasks": tasks})
}

func (s *Server) deleteEpic(w http.ResponseWriter, r *http.Request) {
	uid, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	epic, err := s.Store.EpicByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if epic == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "epic not found"})
		return
	}
	member, err := s.Store.IsMember(r.Context(), epic.ProjectID, uid)
	if err != nil || !member {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}
	if err := s.Store.DeleteEpic(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
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

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write: %v", err)
	}
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}
