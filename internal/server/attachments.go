package server

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"akhflow.ai/akhflow/internal/domain"
)

const maxAttachmentSize = 10 << 20
const maxAttachmentRequestSize = maxAttachmentSize + 64<<10

func (s *Server) attachmentTask(w http.ResponseWriter, r *http.Request) *domain.Task {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad task id"})
		return nil
	}
	userID, err := UserFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return nil
	}
	task, err := s.Store.TaskByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "task lookup failed"})
		return nil
	}
	if task == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return nil
	}
	member, err := s.Store.IsMember(r.Context(), task.ProjectID, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "access check failed"})
		return nil
	}
	if !member {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return nil
	}
	return task
}

func attachmentFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || len(name) > 255 || !utf8.ValidString(name) {
		return ""
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return ""
		}
	}
	return name
}

func (s *Server) listAttachments(w http.ResponseWriter, r *http.Request) {
	task := s.attachmentTask(w, r)
	if task == nil {
		return
	}
	attachments, err := s.Store.ListAttachments(r.Context(), task.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "attachment lookup failed"})
		return
	}
	writeJSON(w, http.StatusOK, attachments)
}

func (s *Server) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	task := s.attachmentTask(w, r)
	if task == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentRequestSize)
	reader, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "multipart form required"})
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "one file is required"})
		return
	}
	filename := attachmentFilename(part.FileName())
	if filename == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filename"})
		return
	}
	content, err := io.ReadAll(io.LimitReader(part, maxAttachmentSize+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read file"})
		return
	}
	if len(content) > maxAttachmentSize {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "file exceeds 10 MiB"})
		return
	}
	_, err = reader.NextPart()
	if err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "one file is required"})
		return
	}
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create attachment"})
		return
	}
	attachment, err := s.Store.CreateAttachment(r.Context(), task.ID, filename, hex.EncodeToString(key), content)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create attachment"})
		return
	}
	writeJSON(w, http.StatusCreated, attachment)
}

func (s *Server) downloadAttachment(w http.ResponseWriter, r *http.Request) {
	task := s.attachmentTask(w, r)
	if task == nil {
		return
	}
	attachmentID, err := strconv.ParseInt(r.PathValue("attachment"), 10, 64)
	if err != nil || attachmentID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad attachment id"})
		return
	}
	attachment, content, err := s.Store.AttachmentContent(r.Context(), task.ID, attachmentID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "attachment lookup failed"})
		return
	}
	if attachment == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Filename}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(content)
}
