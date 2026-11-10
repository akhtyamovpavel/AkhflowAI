package server

import (
	"net/http"
	"strconv"

	"akhflow.ai/akhflow/internal/store"
)

func (s *Server) getAgentUsage(w http.ResponseWriter, r *http.Request) {
	userID, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	project, ok := s.projectBySlug(w, r, userID)
	if !ok {
		return
	}
	days := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		days, err = strconv.Atoi(raw)
		if err != nil || !store.ValidAgentUsageDays(days) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "days must be 0, 7 or 30"})
			return
		}
	}
	usage, err := s.Store.AgentUsage(r.Context(), project.ID, days)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load agent usage"})
		return
	}
	writeJSON(w, http.StatusOK, usage)
}
