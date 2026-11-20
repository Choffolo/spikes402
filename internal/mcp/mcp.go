// Package mcp is a minimal Model Context Protocol server over stdio
// (newline-delimited JSON-RPC 2.0), enough to expose a few tools.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"slices"
)

// Tool is one tool the AI can call.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Call        func(ctx context.Context, args json.RawMessage) (string, error)
}

// Server answers MCP requests.
type Server struct {
	Name, Version string
	Tools         []Tool
}

var knownVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve reads requests from r and writes responses to w until r ends.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if resp := s.handle(ctx, line); resp != nil {
			if err := enc.Encode(resp); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

func (s *Server) handle(ctx context.Context, line []byte) *response {
	var req request
	if err := json.Unmarshal(line, &req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
		return &response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error or invalid request"}}
	}
	if len(req.ID) == 0 { // notification (e.g. notifications/initialized): no answer
		return nil
	}
	resp := &response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		v := knownVersions[0]
		if slices.Contains(knownVersions, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		resp.Result = map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		tools := []map[string]any{}
		for _, t := range s.Tools {
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema})
		}
		resp.Result = map[string]any{"tools": tools}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &rpcError{-32602, "invalid params"}
			break
		}
		i := slices.IndexFunc(s.Tools, func(t Tool) bool { return t.Name == p.Name })
		if i < 0 {
			resp.Error = &rpcError{-32602, "unknown tool " + p.Name}
			break
		}
		if len(p.Arguments) == 0 {
			p.Arguments = json.RawMessage("{}")
		}
		text, err := s.Tools[i].Call(ctx, p.Arguments)
		if err != nil {
			resp.Result = map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true}
		} else {
			resp.Result = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
		}
	default:
		resp.Error = &rpcError{-32601, "method not found: " + req.Method}
	}
	return resp
}
