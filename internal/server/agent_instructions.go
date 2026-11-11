package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

func (s *Server) getAgentInstructions(w http.ResponseWriter, r *http.Request) {
	uid, _ := UserFrom(r.Context())
	project, ok := s.projectBySlug(w, r, uid)
	if !ok {
		return
	}
	value, err := s.Store.AgentInstructions(r.Context(), project.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "instructions unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) putAgentInstructions(w http.ResponseWriter, r *http.Request) {
	uid, _ := UserFrom(r.Context())
	project, ok := s.projectBySlug(w, r, uid)
	if !ok {
		return
	}
	role, err := s.Store.ProjectRole(r.Context(), project.ID, uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "role lookup failed"})
		return
	}
	if role != "owner" && role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 131073))
	if err != nil || len(body) > 131072 || !utf8.Valid(body) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid instructions"})
		return
	}
	var input struct {
		Instructions *string `json:"instructions"`
		Version      *int64  `json:"version"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	err = decoder.Decode(&input)
	if err != nil || input.Instructions == nil || input.Version == nil || *input.Version < 0 || len(*input.Instructions) > 16384 || strings.ContainsRune(*input.Instructions, 0) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid instructions"})
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid instructions"})
		return
	}
	value, err := s.Store.UpdateAgentInstructions(r.Context(), project.ID, uid, *input.Version, *input.Instructions)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "instructions update failed"})
		return
	}
	if value == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "instructions changed or access revoked"})
		return
	}
	writeJSON(w, http.StatusOK, value)
}
