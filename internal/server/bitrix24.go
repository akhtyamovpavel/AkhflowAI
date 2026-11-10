package server

import (
	"encoding/json"
	"net/http"

	"akhflow.ai/akhflow/internal/toolbox"
)

func (s *Server) importBitrixTasks(w http.ResponseWriter, r *http.Request) {
	userID, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var input struct {
		GroupID int64 `json:"group_id"`
	}
	if err := readJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid import parameters"})
		return
	}
	args, err := json.Marshal(map[string]any{"project": r.PathValue("slug"), "group_id": input.GroupID})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "import failed"})
		return
	}
	deps := toolbox.Deps{Store: s.Store, ActorID: userID, Actor: s.Auth.ActorName(r.Context()), Bus: s.Bus}
	result, err := toolbox.ByName("import_bitrix24_tasks").Handler(r.Context(), deps, args)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}
