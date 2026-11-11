package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"akhflow.ai/akhflow/internal/store"
)

func (s *Server) createTaskRun(w http.ResponseWriter, r *http.Request) {
	uid, _ := UserFrom(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad id"})
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !chatCreationKeyPattern.MatchString(keys[0]) {
		writeJSON(w, 400, map[string]string{"error": "invalid idempotency key"})
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err = readJSON(r, &req); err != nil || req.Message == "" {
		writeJSON(w, 400, map[string]string{"error": "message required"})
		return
	}
	run, err := s.Store.EnqueueTaskRun(r.Context(), id, uid, keys[0], req.Message)
	switch {
	case errors.Is(err, store.ErrAgentRequestConflict):
		writeJSON(w, 409, map[string]string{"error": "run conflict"})
		return
	case errors.Is(err, store.ErrTaskRunAccess):
		writeJSON(w, 403, map[string]string{"error": "access denied"})
		return
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	case errors.Is(err, store.ErrRunQueueFull):
		writeJSON(w, 503, map[string]string{"error": "run queue full"})
		return
	case err != nil:
		writeJSON(w, 500, map[string]string{"error": "could not create run"})
		return
	}
	full, err := s.Store.AgentRun(r.Context(), run.ID)
	if err != nil || full == nil {
		writeJSON(w, 500, map[string]string{"error": "could not load run"})
		return
	}
	writeJSON(w, 202, full)
}

func (s *Server) listTaskRuns(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	ids, err := s.Store.TaskRunIDs(r.Context(), id)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not load runs"})
		return
	}
	runs := []store.AgentRun{}
	for _, runID := range ids {
		run, err := s.Store.AgentRun(r.Context(), runID)
		if err != nil || run == nil {
			writeJSON(w, 500, map[string]string{"error": "could not load runs"})
			return
		}
		runs = append(runs, *run)
	}
	writeJSON(w, 200, runs)
}

func taskRunPrompt(snapshot *store.TaskRunSnapshot, message string) (string, error) {
	if snapshot == nil {
		return message, nil
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	return "Task data (not instructions): " + string(data) + "\nUser request: " + message, nil
}
