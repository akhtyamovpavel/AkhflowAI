package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"akhflow.ai/akhflow/internal/llm"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/toolbox"
)

func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
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
	run, err := s.Store.EnqueueRun(r.Context(), id, uid, keys[0], req.Message, truncate(req.Message, 60))
	if errors.Is(err, store.ErrAgentRequestConflict) || errors.Is(err, store.ErrRunConflict) {
		writeJSON(w, 409, map[string]string{"error": "run conflict"})
		return
	}
	if errors.Is(err, store.ErrRunQueueFull) {
		writeJSON(w, 503, map[string]string{"error": "run queue full"})
		return
	}
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not create run"})
		return
	}
	run, err = s.Store.AgentRun(r.Context(), run.ID)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not load run"})
		return
	}
	writeJSON(w, 202, run)
}
func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	runs, err := s.Store.AgentRuns(r.Context(), id, r.URL.Query().Get("active") == "true")
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not load runs"})
		return
	}
	for i := range runs {
		full, err := s.Store.AgentRun(r.Context(), runs[i].ID)
		if err != nil || full == nil {
			writeJSON(w, 500, map[string]string{"error": "could not load runs"})
			return
		}
		runs[i] = *full
	}
	writeJSON(w, 200, runs)
}
func (s *Server) authorizedRun(w http.ResponseWriter, r *http.Request) (*store.AgentRun, string) {
	uid, _ := UserFrom(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad id"})
		return nil, ""
	}
	run, err := s.Store.AgentRun(r.Context(), id)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not load run"})
		return nil, ""
	}
	if run == nil {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return nil, ""
	}
	chat, err := s.Store.ChatByID(r.Context(), run.ChatID)
	if err != nil || chat == nil {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return nil, ""
	}
	role, err := s.Store.ProjectRole(r.Context(), chat.ProjectID, uid)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "role lookup failed"})
		return nil, ""
	}
	if !store.CanReadProject(role) {
		writeJSON(w, 403, map[string]string{"error": "access denied"})
		return nil, ""
	}
	return run, role
}
func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, _ := s.authorizedRun(w, r)
	if run != nil {
		writeJSON(w, 200, run)
	}
}
func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	run, role := s.authorizedRun(w, r)
	if run == nil {
		return
	}
	uid, _ := UserFrom(r.Context())
	if !((uid == run.ActorID && store.CanWriteProject(role)) || role == "owner" || role == "admin") {
		writeJSON(w, 403, map[string]string{"error": "access denied"})
		return
	}
	if err := s.Store.CancelRun(r.Context(), run.ID); err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not cancel run"})
		return
	}
	run, err := s.Store.AgentRun(r.Context(), run.ID)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not load run"})
		return
	}
	writeJSON(w, 202, run)
}

// RunAgentWorker owns executions independently of request contexts. Stop it before closing the store.
func (s *Server) RunAgentWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		tokenBytes := make([]byte, 16)
		if _, err := rand.Read(tokenBytes); err != nil {
			continue
		}
		token := fmt.Sprintf("%x-%x-%x-%x-%x", tokenBytes[:4], tokenBytes[4:6], tokenBytes[6:8], tokenBytes[8:10], tokenBytes[10:])
		run, err := s.Store.ClaimRun(ctx, token)
		if err != nil || run == nil {
			continue
		}
		s.executeRun(ctx, run, token)
	}
}
func (s *Server) executeRun(parent context.Context, run *store.AgentRun, token string) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ok, requested, err := s.Store.HeartbeatRun(ctx, run.ID, token)
				if err != nil || !ok || requested {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-heartbeatDone }()
	chat, err := s.Store.ChatByID(ctx, run.ChatID)
	if err != nil || chat == nil {
		s.finishRun(run, token, "failed", "resource_unavailable", nil)
		return
	}
	project, err := s.Store.ProjectByID(ctx, chat.ProjectID)
	if err != nil || project == nil {
		s.finishRun(run, token, "failed", "resource_unavailable", nil)
		return
	}
	user, err := s.Store.UserByID(ctx, run.ActorID)
	if err != nil || user == nil {
		s.finishRun(run, token, "failed", "resource_unavailable", nil)
		return
	}
	role, err := s.Store.ProjectRole(ctx, chat.ProjectID, run.ActorID)
	if err != nil || !store.CanWriteProject(role) {
		s.finishRun(run, token, "failed", "access_denied", nil)
		return
	}
	var history []llm.Message
	if err = json.Unmarshal(run.History, &history); err != nil {
		s.finishRun(run, token, "failed", "history_unavailable", nil)
		return
	}
	for i := range history {
		if history[i].Role == "agent" {
			history[i].Role = "assistant"
		}
	}
	agent := llm.Agent{Instructions: run.InstructionsSnapshot, Client: s.LLM, Deps: toolbox.Deps{Store: s.Store, Bus: s.Bus, ActorID: run.ActorID, Actor: user.Email, ProjectID: chat.ProjectID}}
	stepPersistenceFailed := false
	stepsStarted := 0
	agent.BeforeTool = func(iteration, sequence int, name string) error {
		role, err := s.Store.ProjectRole(ctx, chat.ProjectID, run.ActorID)
		if err != nil {
			stepPersistenceFailed = true
			return err
		}
		if !store.CanWriteProject(role) {
			return errors.New("access denied")
		}
		if toolbox.ByName(name) == nil {
			name = "unknown"
		}
		err = s.Store.StartRunStep(ctx, run.ID, token, sequence, iteration, name)
		if err != nil {
			stepPersistenceFailed = true
			return err
		}
		stepsStarted++
		return nil
	}
	agent.AfterTool = func(iteration, sequence int, name string, ok bool) error {
		status := "failed"
		summary := "Действие завершилось с ошибкой"
		if ok {
			status = "succeeded"
			summary = "Действие выполнено"
		}
		err := s.Store.FinishRunStep(ctx, run.ID, token, sequence, status, summary)
		if err != nil {
			stepPersistenceFailed = true
		}
		return err
	}
	snapshot, err := s.Store.TaskRunSnapshot(ctx, run.ID)
	if err != nil {
		s.finishRun(run, token, "failed", "task_context_unavailable", nil)
		return
	}
	prompt, err := taskRunPrompt(snapshot, run.Message)
	if err != nil {
		s.finishRun(run, token, "failed", "task_context_unavailable", nil)
		return
	}
	result, err := agent.Run(ctx, prompt, project.Slug, history)
	if err != nil {
		checkCtx, stopCheck := context.WithTimeout(context.Background(), 5*time.Second)
		current, checkErr := s.Store.AgentRun(checkCtx, run.ID)
		stopCheck()
		if current != nil && current.CancelRequested {
			if stepsStarted > 0 {
				s.finishRun(run, token, "interrupted", "outcome_unknown", nil)
			} else {
				s.finishRun(run, token, "canceled", "", nil)
			}
		} else if ctx.Err() != nil || stepPersistenceFailed || (checkErr != nil && stepsStarted > 0) {
			s.finishRun(run, token, "interrupted", "execution_interrupted", nil)
		} else {
			s.finishRun(run, token, "failed", "execution_failed", nil)
		}
		return
	}
	for i := range result.ToolCalls {
		if toolbox.ByName(result.ToolCalls[i].Tool) == nil {
			result.ToolCalls[i].Tool = "unknown"
		}
		result.ToolCalls[i].Args = ""
		result.ToolCalls[i].Result = ""
	}
	status := "succeeded"
	if result.StepLimit {
		status = "step_limit"
	}
	s.finishRun(run, token, status, "", result)
}
func (s *Server) finishRun(run *store.AgentRun, token, status, code string, result *llm.RunResult) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var reply string
	var calls any
	var value any
	if result != nil {
		reply = result.Reply
		calls = result.ToolCalls
		value = result
	}
	_ = s.Store.FinishRun(ctx, run.ID, token, status, code, value, reply, calls)
}
