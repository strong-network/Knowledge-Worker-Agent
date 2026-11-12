// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package mcppkbrain exposes PKBrain's bounded, on-demand imported-history
// retrieval API as native OpenCode tools. The API credentials stay in PKBrain;
// this bridge sends only search/fetch arguments over the private Compose network.
package mcppkbrain

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxFrame = 1 << 20

type request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type Server struct {
	BaseURL string
	In      io.Reader
	Out     io.Writer
	Client  *http.Client
}

func ValidateBaseURL(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("PKBrain URL must be an HTTP(S) service URL without credentials or query parameters")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func New(baseURL string, in io.Reader, out io.Writer) (*Server, error) {
	validatedURL, err := ValidateBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	return &Server{
		BaseURL: validatedURL,
		In:      in,
		Out:     out,
		Client:  &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (s *Server) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(s.In)
	scanner.Buffer(make([]byte, 64*1024), maxFrame)
	writer := bufio.NewWriter(s.Out)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			s.write(writer, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "parse error"}})
			continue
		}
		if len(bytes.TrimSpace(req.ID)) == 0 || string(bytes.TrimSpace(req.ID)) == "null" {
			continue
		}
		s.write(writer, s.dispatch(ctx, req))
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("PKBrain MCP input failed")
	}
	return nil
}

func (s *Server) write(w *bufio.Writer, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	_, _ = w.Write(data)
	_ = w.WriteByte('\n')
	_ = w.Flush()
}

func (s *Server) dispatch(ctx context.Context, req request) map[string]any {
	result := func(value any) map[string]any { return map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": value} }
	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		if params.ProtocolVersion == "" {
			params.ProtocolVersion = "2025-11-25"
		}
		return result(map[string]any{
			"protocolVersion": params.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "pkbrain-history", "version": "1"},
		})
	case "ping":
		return result(map[string]any{})
	case "tools/list":
		return result(map[string]any{"tools": toolDefs()})
	case "tools/call":
		return s.callTool(ctx, req)
	default:
		return map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}}
	}
}

func (s *Server) callTool(ctx context.Context, req request) map[string]any {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid tool arguments")
	}
	path, body, err := toolRequest(params.Name, params.Arguments)
	if err != nil {
		return rpcError(req.ID, -32602, err.Error())
	}
	endpoint := s.BaseURL + path
	encoded, err := json.Marshal(body)
	if err != nil {
		return toolResult(req.ID, "PKBrain retrieval request could not be encoded.", true)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return toolResult(req.ID, "PKBrain retrieval is unavailable.", true)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.Client.Do(request)
	if err != nil {
		return toolResult(req.ID, "PKBrain retrieval is unavailable on the local Compose network.", true)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxFrame+1))
	if err != nil || len(data) > maxFrame {
		return toolResult(req.ID, "PKBrain returned a response larger than the MCP safety limit.", true)
	}
	if response.StatusCode != http.StatusOK {
		// Never forward internal error bodies, which may echo request metadata.
		return toolResult(req.ID, fmt.Sprintf("PKBrain retrieval returned HTTP %d.", response.StatusCode), true)
	}
	return toolResult(req.ID, string(data), false)
}

func toolRequest(name string, args map[string]any) (string, map[string]any, error) {
	if args == nil {
		args = map[string]any{}
	}
	getString := func(key string) string {
		value, _ := args[key].(string)
		return strings.TrimSpace(value)
	}
	getInt := func(key string, fallback int) (int, error) {
		value, ok := args[key]
		if !ok || value == nil {
			return fallback, nil
		}
		switch typed := value.(type) {
		case float64:
			if typed != float64(int(typed)) {
				return 0, fmt.Errorf("%s must be an integer", key)
			}
			return int(typed), nil
		case int:
			return typed, nil
		default:
			return 0, fmt.Errorf("%s must be an integer", key)
		}
	}
	switch name {
	case "pkbrain_search":
		query := getString("query")
		if query == "" || len(query) > 500 {
			return "", nil, fmt.Errorf("query must contain 1 to 500 characters")
		}
		limit, err := getInt("limit", 5)
		if err != nil {
			return "", nil, err
		}
		maxBytes, err := getInt("max_bytes", 8_192)
		if err != nil {
			return "", nil, err
		}
		if limit < 1 || limit > 10 || maxBytes < 2_048 || maxBytes > 16_384 {
			return "", nil, fmt.Errorf("search limits must be at most 10 excerpts and 16384 bytes")
		}
		body := map[string]any{"query": query, "limit": limit, "max_bytes": maxBytes}
		if project := getString("project"); project != "" {
			if len(project) > 200 {
				return "", nil, fmt.Errorf("project must contain at most 200 characters")
			}
			body["project"] = project
		}
		return "/internal/v1/retrieve", body, nil
	case "pkbrain_fetch":
		sourceID := getString("source_id")
		if sourceID == "" || len(sourceID) > 500 {
			return "", nil, fmt.Errorf("source_id must contain 1 to 500 characters")
		}
		offset, err := getInt("offset", 0)
		if err != nil {
			return "", nil, err
		}
		maxBytes, err := getInt("max_bytes", 16_384)
		if err != nil {
			return "", nil, err
		}
		if offset < 0 || maxBytes < 2_048 || maxBytes > 65_536 {
			return "", nil, fmt.Errorf("fetch offset must be non-negative and max_bytes must be at most 65536")
		}
		return "/internal/v1/discussion", map[string]any{"source_id": sourceID, "offset": offset, "max_bytes": maxBytes}, nil
	default:
		return "", nil, fmt.Errorf("unknown PKBrain tool")
	}
}

func toolResult(id json.RawMessage, text string, isError bool) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
		"isError": isError,
		"content": []any{map[string]any{"type": "text", "text": text}},
	}}
}

func rpcError(id json.RawMessage, code int, message string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}
}
