package server

import (
	"net/http"
	"strconv"

	"akhflow.ai/akhflow/internal/store"
)

// sseAccess accepts a query token only on the events endpoint. An explicit
// Authorization header always takes precedence, even when it is invalid.
func (s *Server) sseAccess(next http.HandlerFunc) http.Handler {
	handler := s.projectAccess("project", false, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded := r.Clone(r.Context())
		query := forwarded.URL.Query()
		token := query.Get("token")
		query.Del("token")
		forwarded.URL.RawQuery = query.Encode()
		if forwarded.Header.Get("Authorization") == "" && token != "" {
			forwarded.Header.Set("Authorization", "Bearer "+token)
		}
		handler.ServeHTTP(w, forwarded)
	})
}

func (s *Server) projectAccess(resource string, write bool, next http.HandlerFunc) http.Handler {
	return s.Auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, err := UserFrom(r.Context())
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			return
		}
		var projectID int64
		switch resource {
		case "project":
			project, err := s.Store.ProjectBySlug(r.Context(), r.PathValue("slug"))
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "project lookup failed"})
				return
			}
			if project != nil {
				projectID = project.ID
			}
		case "task", "epic", "chat":
			id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
				return
			}
			switch resource {
			case "task":
				task, lookupErr := s.Store.TaskByID(r.Context(), id)
				err = lookupErr
				if task != nil {
					projectID = task.ProjectID
				}
			case "epic":
				epic, lookupErr := s.Store.EpicByID(r.Context(), id)
				err = lookupErr
				if epic != nil {
					projectID = epic.ProjectID
				}
			case "chat":
				chat, lookupErr := s.Store.ChatByID(r.Context(), id)
				err = lookupErr
				if chat != nil {
					projectID = chat.ProjectID
				}
			}
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resource lookup failed"})
				return
			}
		}
		if projectID == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		role, err := s.Store.ProjectRole(r.Context(), projectID, userID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "role lookup failed"})
			return
		}
		if !store.CanReadProject(role) || (write && !store.CanWriteProject(role)) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
			return
		}
		next(w, r)
	}))
}

func (s *Server) setMemberRole(w http.ResponseWriter, r *http.Request) {
	actorID, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	project, ok := s.projectBySlug(w, r, actorID)
	if !ok {
		return
	}
	actorRole, err := s.Store.ProjectRole(r.Context(), project.ID, actorID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "role lookup failed"})
		return
	}
	if actorRole != "owner" && actorRole != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}
	var input struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := readJSON(r, &input); err != nil || (input.Role != "viewer" && input.Role != "editor") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email and valid role required"})
		return
	}
	user, err := s.Store.UserByEmail(r.Context(), input.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "user lookup failed"})
		return
	}
	if user == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	updated, err := s.Store.SetProjectMemberRole(r.Context(), project.ID, actorID, user.ID, input.Role)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "role update failed"})
		return
	}
	if !updated {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) getProjectRole(w http.ResponseWriter, r *http.Request) {
	userID, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	project, ok := s.projectBySlug(w, r, userID)
	if !ok {
		return
	}
	role, err := s.Store.ProjectRole(r.Context(), project.ID, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "role lookup failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"role": role})
}
