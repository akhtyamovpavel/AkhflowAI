package server

import "net/http"

func (s *Server) dashboardPreferences(w http.ResponseWriter, r *http.Request) {
	userID, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	project, ok := s.projectBySlug(w, r, userID)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		hidden, err := s.Store.DashboardHiddenWidgets(r.Context(), project.ID, userID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load dashboard settings"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"hidden_widgets": hidden})
		return
	}
	var request struct {
		HiddenWidgets *[]string `json:"hidden_widgets"`
	}
	if err := readJSON(r, &request); err != nil || request.HiddenWidgets == nil || len(*request.HiddenWidgets) > 4 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid widgets"})
		return
	}
	allowed := map[string]bool{"statuses": true, "priorities": true, "assignees": true, "epics": true}
	for _, widget := range *request.HiddenWidgets {
		if !allowed[widget] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid widgets"})
			return
		}
		delete(allowed, widget)
	}
	if err := s.Store.SetDashboardHiddenWidgets(r.Context(), project.ID, userID, *request.HiddenWidgets); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save dashboard settings"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hidden_widgets": *request.HiddenWidgets})
}
