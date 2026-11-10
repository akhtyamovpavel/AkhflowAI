package bitrix24

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"akhflow.ai/akhflow/internal/domain"
)

type Task struct {
	ID          string
	Title       string
	Description string
	Status      string
	Priority    string
	DueDate     *time.Time
}

type Client struct {
	URL  string
	HTTP *http.Client
}

func value(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// Fetch returns all pages visible to the configured webhook user.
func (c Client) Fetch(ctx context.Context, groupID int64) (string, []Task, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", nil, errors.New("BITRIX24_WEBHOOK_URL must be an HTTPS webhook base URL")
	}
	if groupID < 0 {
		return "", nil, errors.New("invalid Bitrix24 group ID")
	}
	portal := strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/") + "/tasks.task.list.json"
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	// Do not follow redirects: the webhook URL embeds a credential.
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	tasks := []Task{}
	start := 0
	for page := 0; page < 200; page++ {
		params := map[string]any{"start": start, "order": map[string]string{"ID": "asc"}, "select": []string{"ID", "TITLE", "DESCRIPTION", "STATUS", "PRIORITY", "DEADLINE"}}
		if groupID > 0 {
			params["filter"] = map[string]int64{"GROUP_ID": groupID}
		}
		body, err := json.Marshal(params)
		if err != nil {
			return "", nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
		if err != nil {
			return "", nil, errors.New("invalid Bitrix24 webhook configuration")
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := clientCopy.Do(req)
		if err != nil {
			return "", nil, errors.New("Bitrix24 request failed")
		}
		var result struct {
			Result struct {
				Tasks []struct {
					ID          json.RawMessage `json:"id"`
					Title       string          `json:"title"`
					Description string          `json:"description"`
					Status      json.RawMessage `json:"status"`
					Priority    json.RawMessage `json:"priority"`
					Deadline    string          `json:"deadline"`
				} `json:"tasks"`
			} `json:"result"`
			Next  json.RawMessage `json:"next"`
			Error string          `json:"error"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 5<<20)).Decode(&result)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || decodeErr != nil || result.Error != "" || result.Result.Tasks == nil {
			return "", nil, fmt.Errorf("Bitrix24 returned an invalid response (HTTP %d)", resp.StatusCode)
		}
		for _, item := range result.Result.Tasks {
			id := value(item.ID)
			if id == "" || strings.TrimSpace(item.Title) == "" {
				return "", nil, errors.New("Bitrix24 task lacks ID or title")
			}
			task := Task{ID: id, Title: item.Title, Description: item.Description, Status: domain.StatusTodo, Priority: domain.PriorityMedium}
			switch value(item.Status) {
			case "3":
				task.Status = domain.StatusInProgress
			case "5":
				task.Status = domain.StatusDone
			case "6":
				task.Status = domain.StatusBacklog
			}
			if value(item.Priority) == "2" {
				task.Priority = domain.PriorityHigh
			}
			if item.Deadline != "" {
				due, err := time.Parse(time.RFC3339, item.Deadline)
				if err != nil {
					return "", nil, errors.New("invalid Bitrix24 deadline")
				}
				task.DueDate = &due
			}
			tasks = append(tasks, task)
		}
		next := value(result.Next)
		if next == "" {
			return portal, tasks, nil
		}
		n, err := strconv.Atoi(next)
		if err != nil || n <= start {
			return "", nil, errors.New("invalid Bitrix24 pagination")
		}
		start = n
	}
	return "", nil, errors.New("Bitrix24 page limit exceeded")
}
