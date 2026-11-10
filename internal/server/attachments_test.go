package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/domain"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/toolbox"
)

func TestAttachmentHTTP(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AKHFLOW_TEST_DATABASE_URL must point to an isolated database")
	}
	ctx := context.Background()
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	ownerID, err := db.CreateUser(ctx, fmt.Sprintf("attachment-owner-%d@example.invalid", suffix), "Owner", "unused")
	if err != nil {
		t.Fatal(err)
	}
	outsiderID, err := db.CreateUser(ctx, fmt.Sprintf("attachment-other-%d@example.invalid", suffix), "Other", "unused")
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := db.CreateProject(ctx, fmt.Sprintf("attachment-%d", suffix), "Test", ownerID)
	if err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ProjectID: projectID, AuthorID: ownerID, Title: "Attachment", Status: "todo", Priority: "medium"}
	taskID, err := db.CreateTask(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	otherTaskID, err := db.CreateTask(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	auth := &Auth{Store: db, Secret: []byte("test-signing-key"), TokenTTL: time.Hour}
	ownerToken, err := auth.CreateToken(ownerID)
	if err != nil {
		t.Fatal(err)
	}
	outsiderToken, err := auth.CreateToken(outsiderID)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(db, auth, nil, nil).Handler()
	request := func(method, endpoint, token string, body *bytes.Buffer, contentType string) *httptest.ResponseRecorder {
		if body == nil {
			body = &bytes.Buffer{}
		}
		req := httptest.NewRequest(method, endpoint, body)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	multipartFile := func(filename string, data []byte, count int) (*bytes.Buffer, string) {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		for index := 0; index < count; index++ {
			part, err := writer.CreateFormFile("file", filename)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write(data); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return body, writer.FormDataContentType()
	}
	endpoint := fmt.Sprintf("/api/tasks/%d/attachments", taskID)
	body, contentType := multipartFile("report.txt", []byte("private content"), 1)
	response := request("POST", endpoint, ownerToken, body, contentType)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", response.Code, response.Body.String())
	}
	var attachment domain.Attachment
	if err := json.Unmarshal(response.Body.Bytes(), &attachment); err != nil {
		t.Fatal(err)
	}
	if attachment.Filename != "report.txt" || attachment.SizeBytes != int64(len("private content")) {
		t.Fatalf("wrong metadata: %+v", attachment)
	}
	download := fmt.Sprintf("%s/%d", endpoint, attachment.ID)
	response = request("GET", download, ownerToken, nil, "")
	if response.Code != http.StatusOK || response.Body.String() != "private content" {
		t.Fatalf("download: %d %q", response.Code, response.Body.String())
	}
	if !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment;") || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download must not execute inline: %v", response.Header())
	}
	response = request("GET", endpoint, ownerToken, nil, "")
	var listed []domain.Attachment
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil || len(listed) != 1 {
		t.Fatalf("list: %d %s %v", response.Code, response.Body.String(), err)
	}
	for _, target := range []string{endpoint, download} {
		response = request("GET", target, outsiderToken, nil, "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("outsider read: %s status %d", target, response.Code)
		}
	}
	body, contentType = multipartFile("unauthorized.txt", []byte("x"), 1)
	if response = request("POST", endpoint, outsiderToken, body, contentType); response.Code != http.StatusNotFound {
		t.Fatalf("outsider upload: %d", response.Code)
	}
	wrongTask := fmt.Sprintf("/api/tasks/%d/attachments/%d", otherTaskID, attachment.ID)
	if response = request("GET", wrongTask, ownerToken, nil, ""); response.Code != http.StatusNotFound {
		t.Fatalf("cross-task download: %d", response.Code)
	}
	body, contentType = multipartFile("too-large.bin", bytes.Repeat([]byte("x"), maxAttachmentSize+1), 1)
	if response = request("POST", endpoint, ownerToken, body, contentType); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload: %d", response.Code)
	}
	body, contentType = multipartFile("one.txt", []byte("x"), 2)
	if response = request("POST", endpoint, ownerToken, body, contentType); response.Code != http.StatusBadRequest {
		t.Fatalf("multiple files: %d", response.Code)
	}
	if attachmentFilename(`C:\private\file.txt`) != "file.txt" || attachmentFilename("bad\nname") != "" {
		t.Fatal("unsafe filenames not handled")
	}
	args := json.RawMessage(fmt.Sprintf(`{"task_id":%d}`, taskID))
	for _, tool := range toolbox.Registry {
		if tool.Name != "list_attachments" {
			continue
		}
		result, err := tool.Handler(ctx, toolbox.Deps{Store: db, ActorID: ownerID}, args)
		if err != nil || !result.Ok {
			t.Fatalf("owner tool: %+v %v", result, err)
		}
		if _, err := tool.Handler(ctx, toolbox.Deps{Store: db, ActorID: outsiderID}, args); err == nil {
			t.Fatal("outsider listed attachments through MCP")
		}
		return
	}
	t.Fatal("list_attachments not registered")
}
