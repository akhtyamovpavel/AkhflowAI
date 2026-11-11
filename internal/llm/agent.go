package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"akhflow.ai/akhflow/internal/toolbox"
)

const maxIterations = 8

type AgentDeps struct {
	Store interface {
		Audit(ctx context.Context, actor, action, entity string, entityID *int64, payload map[string]any) error
	}
	ToolDeps toolbox.Deps
}

type Agent struct {
	Client     *Client
	Deps       toolbox.Deps
	BeforeTool func(iteration, sequence int, name string) error
	AfterTool  func(iteration, sequence int, name string, ok bool) error
}

type RunResult struct {
	Reply      string         `json:"reply"`
	ToolCalls  []ToolCallLog  `json:"tool_calls"`
	Iterations int            `json:"iterations"`
	Metrics    map[string]int `json:"metrics,omitempty"`
	StepLimit  bool           `json:"-"`
}

type ToolCallLog struct {
	Tool   string `json:"tool"`
	Args   string `json:"args"`
	Result string `json:"result"`
}

// Run executes the agent loop. history contains previous turns of the chat
// (text only, oldest first); tool-call mechanics are not reconstructed.
func (a *Agent) Run(ctx context.Context, userPrompt string, projectSlug string, history []Message) (*RunResult, error) {
	system := "You are the Akhflow-AI agent. You manage tasks in the Akhflow tracker."
	if projectSlug != "" {
		system += " The user works in project \"" + projectSlug + "\": use it as the project for tools when the user does not name one explicitly."
	}
	system += " Use the provided tools to fulfill user requests. Always call tools instead of just describing actions."
	system += " After completing tool calls, reply briefly in Russian with a concise summary of actions taken"
	system += " (task numbers, titles, statuses), e.g. «Задача #5 перемещена в done»."
	system += " NEVER dump raw JSON, tool results or full task lists in the reply."
	tools := make([]ToolDef, 0, len(toolbox.Registry))
	for _, t := range toolbox.Registry {
		tools = append(tools, ToolDef{
			Type: "function",
			Function: FunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Schema,
			},
		})
	}
	messages := []Message{
		{Role: "system", Content: system},
	}
	messages = append(messages, history...)
	messages = append(messages, Message{Role: "user", Content: userPrompt})
	var log []ToolCallLog
	iterations := 0
	for iterations = 1; iterations <= maxIterations; iterations++ {
		resp, err := a.Client.Chat(ctx, ChatRequest{
			Model:    a.Client.Model,
			Messages: messages,
			Tools:    tools,
		})
		if err != nil {
			return nil, err
		}
		if len(resp.Choices) == 0 {
			return nil, errors.New("empty response from model")
		}
		msg := resp.Choices[0].Message
		messages = append(messages, Message{
			Role:      "assistant",
			Content:   msg.Content,
			ToolCalls: msg.ToolCalls,
		})
		if len(msg.ToolCalls) == 0 {
			return &RunResult{Reply: msg.Content, ToolCalls: log, Iterations: iterations}, nil
		}
		for _, tc := range msg.ToolCalls {
			sequence := len(log) + 1
			if a.BeforeTool != nil {
				if err := a.BeforeTool(iterations, sequence, tc.Function.Name); err != nil {
					return nil, err
				}
			}
			tool := toolbox.ByName(tc.Function.Name)
			var args json.RawMessage = json.RawMessage(tc.Function.Arguments)
			var res toolbox.Result
			if tool == nil {
				res = toolbox.Result{Ok: false, Message: fmt.Sprintf("unknown tool %q", tc.Function.Name)}
			} else {
				r, err := tool.Handler(ctx, a.Deps, args)
				if err != nil {
					res = toolbox.Result{Ok: false, Message: err.Error()}
				} else {
					res = r
				}
			}
			payload, _ := json.Marshal(res)
			if a.AfterTool != nil {
				if err := a.AfterTool(iterations, sequence, tc.Function.Name, res.Ok); err != nil {
					return nil, err
				}
			}
			log = append(log, ToolCallLog{Tool: tc.Function.Name, Args: string(args), Result: string(payload)})
			messages = append(messages, Message{
				Role:       "tool",
				Content:    string(payload),
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
			})
		}
	}
	return &RunResult{
		Reply:      "Превышено число шагов агента, последний ответ модели не получен",
		ToolCalls:  log,
		Iterations: iterations - 1,
		StepLimit:  true,
	}, nil
}
