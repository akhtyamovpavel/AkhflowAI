package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"akhflow.ai/akhflow/internal/events"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/toolbox"
)

const ProtocolVersion = "2025-06-18"

type Server struct {
	Store   *store.Store
	UserID  int64
	Actor   string
	Version string
	APIKey  string
	Bus     *events.Bus
}

type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func (s *Server) handle(req *RPCRequest) (*RPCResponse, error) {
	switch req.Method {
	case "initialize":
		return response(req.ID, map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "akhflow", "version": s.Version},
		}), nil
	case "ping":
		return response(req.ID, map[string]any{}), nil
	case "tools/list":
		tools := make([]map[string]any, 0, len(toolbox.Registry))
		for _, t := range toolbox.Registry {
			tools = append(tools, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": t.Schema,
			})
		}
		return response(req.ID, map[string]any{"tools": tools}), nil
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, err
		}
		tool := toolbox.ByName(params.Name)
		if tool == nil {
			return nil, errUnknownTool(params.Name)
		}
		res, err := tool.Handler(context.Background(), toolbox.Deps{
			Store:   s.Store,
			ActorID: s.UserID,
			Actor:   s.Actor,
			Bus:     s.Bus,
		}, params.Arguments)
		if err != nil {
			res = toolbox.Result{Ok: false, Message: err.Error()}
		}
		payload, _ := json.Marshal(res)
		return response(req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(payload)}},
			"isError": !res.Ok,
		}), nil
	case "resources/list":
		return response(req.ID, map[string]any{"resources": []any{}}), nil
	case "prompts/list":
		return response(req.ID, map[string]any{"prompts": []any{}}), nil
	case "notifications/initialized", "notifications/cancelled", "notifications/roots/list_changed":
		return nil, nil
	default:
		return nil, errMethodNotFound
	}
}

func response(id json.RawMessage, result any) *RPCResponse {
	return &RPCResponse{JSONRPC: "2.0", ID: id, Result: result}
}

type mcpError struct{ msg string }

func (e *mcpError) Error() string { return e.msg }

func errUnknownTool(name string) error {
	return &mcpError{msg: "unknown tool: " + name}
}

var errMethodNotFound = &mcpError{msg: "method not found"}

func (s *Server) ServeStdio() error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var req RPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			enc.Encode(&RPCResponse{JSONRPC: "2.0", ID: nil, Error: &RPCError{Code: -32700, Message: "parse error"}})
			continue
		}
		resp, err := s.handle(&req)
		if err != nil {
			code := -32603
			var me *mcpError
			if ok := asMCPError(err, &me); ok && me == errMethodNotFound {
				code = -32601
			}
			resp = &RPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: code, Message: err.Error()}}
		}
		if resp == nil {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func asMCPError(err error, target **mcpError) bool {
	me, ok := err.(*mcpError)
	if ok {
		*target = me
	}
	return ok
}

func (s *Server) authorized(r *http.Request) bool {
	if s.APIKey == "" {
		return true
	}
	if key := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); key == s.APIKey {
		return true
	}
	return r.Header.Get("X-MCP-API-Key") == s.APIKey
}

func (s *Server) HTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !s.authorized(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(&RPCResponse{JSONRPC: "2.0", Error: &RPCError{Code: -32600, Message: "unauthorized"}})
			return
		}
		var req RPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(&RPCResponse{JSONRPC: "2.0", Error: &RPCError{Code: -32700, Message: "parse error"}})
			return
		}
		resp, err := s.handle(&req)
		if err != nil {
			code := -32603
			var me *mcpError
			if ok := asMCPError(err, &me); ok && me == errMethodNotFound {
				code = -32601
			}
			resp = &RPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: code, Message: err.Error()}}
		}
		if resp == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
}
