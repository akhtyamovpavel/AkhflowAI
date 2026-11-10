package toolbox

import (
	"context"
	"encoding/json"
	"fmt"

	"akhflow.ai/akhflow/internal/store"
)

func init() {
	Registry = append(Registry, Tool{
		Name:        "get_agent_usage",
		Description: "Summarize saved project chat requests, agent replies and tool calls for 7, 30 or all days.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project": map[string]any{"type": "string"},
				"days":    map[string]any{"type": "integer", "enum": []int{0, 7, 30}, "description": "0 means all time; default 30"},
			},
			"required": []string{"project"},
		},
		Handler: getAgentUsage,
	})
}

func getAgentUsage(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
	input := struct {
		Project string `json:"project"`
		Days    *int   `json:"days"`
	}{}
	if err := json.Unmarshal(args, &input); err != nil {
		return Result{}, err
	}
	days := 30
	if input.Days != nil {
		days = *input.Days
	}
	if !store.ValidAgentUsageDays(days) {
		return Result{}, fmt.Errorf("days must be 0, 7 or 30")
	}
	project, err := projectBySlug(ctx, deps, input.Project)
	if err != nil {
		return Result{}, err
	}
	usage, err := deps.Store.AgentUsage(ctx, project.ID, days)
	if err != nil {
		return Result{}, err
	}
	return Result{Ok: true, Data: usage}, nil
}
