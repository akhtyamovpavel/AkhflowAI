package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"akhflow.ai/akhflow/internal/toolbox"
)

func (s *Server) importTrello(w http.ResponseWriter, r *http.Request) {
	actorID, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	project, err := s.Store.ProjectBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "project lookup failed"})
		return
	}
	if project == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	member, err := s.Store.IsMember(r.Context(), project.ID, actorID)
	if err != nil || !member {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}
	var input struct {
		BoardID string `json:"board_id"`
	}
	if err := readJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	args, err := json.Marshal(map[string]string{"project": r.PathValue("slug"), "board_id": input.BoardID})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	deps := toolbox.Deps{Store: s.Store, ActorID: actorID, Actor: s.Auth.ActorName(r.Context()), Bus: s.Bus}
	result, err := toolbox.ByName("import_trello").Handler(r.Context(), deps, args)
	if err != nil {
		if errors.Is(err, toolbox.ErrTrelloAccess) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}
