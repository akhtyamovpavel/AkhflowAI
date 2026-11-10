package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

var (
	taskProject  string
	taskStatus   string
	taskTitle    string
	taskDesc     string
	taskPriority string
	taskDue      string
	taskAssignee string
)

var taskCmd = &cobra.Command{Use: "task", Short: "Manage tasks via API"}

var taskListCmd = &cobra.Command{
	Use:   "list",
	Short: "List tasks in a project",
	RunE: func(cmd *cobra.Command, args []string) error {
		path := fmt.Sprintf("/api/projects/%s/tasks", taskProject)
		if taskStatus != "" {
			path += "?status=" + taskStatus
		}
		var tasks []struct {
			ID       int    `json:"id"`
			Title    string `json:"title"`
			Status   string `json:"status"`
			Priority string `json:"priority"`
		}
		if err := apiRequest(http.MethodGet, path, nil, &tasks); err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 2, 2, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tSTATUS\tPRIORITY\tTITLE")
		for _, t := range tasks {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", t.ID, t.Status, t.Priority, t.Title)
		}
		return w.Flush()
	},
}

var taskCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a task",
	RunE: func(cmd *cobra.Command, args []string) error {
		if taskTitle == "" {
			return fmt.Errorf("--title is required")
		}
		body := map[string]any{
			"title":       taskTitle,
			"description": taskDesc,
			"priority":    taskPriority,
		}
		if taskDue != "" {
			if _, err := time.Parse(time.DateOnly, taskDue); err != nil {
				return fmt.Errorf("bad --due format, use YYYY-MM-DD")
			}
			body["due_date"] = taskDue + "T00:00:00Z"
		}
		var out map[string]any
		if err := apiRequest(http.MethodPost, fmt.Sprintf("/api/projects/%s/tasks", taskProject), body, &out); err != nil {
			return err
		}
		fmt.Printf("created task %v\n", out["id"])
		return nil
	},
}

var taskMoveCmd = &cobra.Command{
	Use:   "move <id> --status <status>",
	Short: "Move task to another status",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if taskStatus == "" {
			return fmt.Errorf("--status is required")
		}
		body := map[string]any{"status": taskStatus}
		var out map[string]any
		return apiRequest(http.MethodPatch, "/api/tasks/"+args[0], body, &out)
	},
}

var taskDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a task",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var out map[string]any
		return apiRequest(http.MethodDelete, "/api/tasks/"+args[0], nil, &out)
	},
}

var taskGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get task details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var out map[string]any
		if err := apiRequest(http.MethodGet, "/api/tasks/"+args[0], nil, &out); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{taskListCmd, taskCreateCmd, taskMoveCmd} {
		c.Flags().StringVarP(&taskProject, "project", "p", "", "project slug (required)")
	}
	taskListCmd.Flags().StringVar(&taskStatus, "status", "", "filter by status")
	taskCreateCmd.Flags().StringVar(&taskTitle, "title", "", "task title")
	taskCreateCmd.Flags().StringVar(&taskDesc, "description", "", "task description")
	taskCreateCmd.Flags().StringVar(&taskPriority, "priority", "medium", "priority: low|medium|high")
	taskCreateCmd.Flags().StringVar(&taskDue, "due", "", "due date YYYY-MM-DD")
	taskCreateCmd.Flags().StringVar(&taskAssignee, "assignee", "", "assignee email")
	taskMoveCmd.Flags().StringVar(&taskStatus, "status", "", "target status")
	taskCmd.AddCommand(taskListCmd, taskCreateCmd, taskMoveCmd, taskGetCmd, taskDeleteCmd)
}

func apiRequest(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, apiURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("api error %d: %s", resp.StatusCode, string(data))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
