package server

import "net/http"

func (s *Server) getEmailNotifications(w http.ResponseWriter, r *http.Request) {
	userID, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	enabled, err := s.Store.EmailNotificationsEnabled(r.Context(), userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load email settings"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": enabled, "available": s.EmailAvailable})
}

func (s *Server) putEmailNotifications(w http.ResponseWriter, r *http.Request) {
	userID, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var request struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &request); err != nil || request.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "enabled must be a boolean"})
		return
	}
	if *request.Enabled && !s.EmailAvailable {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "email is not configured"})
		return
	}
	if err := s.Store.SetEmailNotifications(r.Context(), userID, *request.Enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save email settings"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": *request.Enabled, "available": s.EmailAvailable})
}
