package server

import (
	"encoding/json"
	"net/http"

	"akhflow.ai/akhflow/internal/akhcheck"
	"akhflow.ai/akhflow/internal/toolbox"
)

func (s *Server) importAkhcheckCourse(w http.ResponseWriter, r *http.Request) {
	id, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	project, ok := s.projectBySlug(w, r, id)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var course json.RawMessage
	if err := readJSON(r, &course); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid course JSON"})
		return
	}
	if _, err := akhcheck.ParseCourse(course); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	input, err := json.Marshal(map[string]any{"project": project.Slug, "course": course})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cannot prepare import"})
		return
	}
	tool := toolbox.ByName("import_akhcheck_course")
	result, err := tool.Handler(r.Context(), toolbox.Deps{Store: s.Store, ActorID: id, Actor: s.Auth.ActorName(r.Context()), Bus: s.Bus}, input)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "import failed"})
		return
	}
	writeJSON(w, http.StatusOK, result.Data)
}
