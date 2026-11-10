package cli

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

var agentCmd = &cobra.Command{
	Use:   "agent <message>",
	Short: "Run AI agent against the tracker",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body := map[string]any{"message": joinArgs(args)}
		var out struct {
			Reply     string `json:"reply"`
			ToolCalls []struct {
				Tool   string `json:"tool"`
				Args   string `json:"args"`
				Result string `json:"result"`
			} `json:"tool_calls"`
			Iterations int `json:"iterations"`
		}
		if err := apiRequest(http.MethodPost, "/api/agent/chat", body, &out); err != nil {
			return err
		}
		for _, tc := range out.ToolCalls {
			fmt.Printf("  [%s] %s -> %s\n", tc.Tool, tc.Args, briefResult(tc.Result))
		}
		fmt.Printf("agent (%d steps): %s\n", out.Iterations, out.Reply)
		return nil
	},
}

// briefResult turns a raw tool result JSON into a one-line summary
// (message field, item count, or truncated payload).
func briefResult(raw string) string {
	var r struct {
		Ok      bool            `json:"ok"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return raw
	}
	if r.Message != "" {
		return r.Message
	}
	var arr []any
	if err := json.Unmarshal(r.Data, &arr); err == nil {
		return fmt.Sprintf("%d записей", len(arr))
	}
	s := string(r.Data)
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
