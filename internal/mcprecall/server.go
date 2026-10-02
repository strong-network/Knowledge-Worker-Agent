// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcprecall

// Cross-session retrieval: the `recall` MCP server.
//
// This is a thin bridge, deliberately. It speaks JSON-RPC 2.0 on stdio to
// opencode, and HTTP GET to the already-running web server. It holds no
// database handle and contains no retrieval logic, so:
//
//   - it cannot write to anything, by construction rather than by discipline;
//   - the JSON shapes the agent sees are produced in exactly one place
//     (internal/recall), which is what makes them a stable contract.
//
// The protocol details below were established by running a probe server
// against a real opencode 1.18.30 and recording every frame it sent, rather
// than read from a specification. Three observations shaped this code:
//
//  1. **Framing is newline-delimited JSON**, not the LSP-style
//     `Content-Length:` header framing used by some MCP transports.
//  2. **The first request id is 0.** Treating a falsy id as "this is a
//     notification" would silently drop the entire `initialize` handshake,
//     which is why isNotification tests for an *absent* id.
//  3. **opencode announces protocolVersion "2025-11-25"** and expects it
//     echoed. We echo whatever the client sends rather than hard-coding a
//     version, so a client upgrade cannot strand this server.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// fallbackProtocolVersion is used only when a client omits protocolVersion.
const fallbackProtocolVersion = "2025-11-25"

// maxLine bounds a single JSON-RPC frame. A `read` result carrying a long
// transcript is the big one; 8 MB is comfortably above the largest measured
// session and still refuses a runaway.
const maxLine = 8 << 20

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// isNotification reports whether a frame is a notification (no reply expected).
//
// The test is presence of the id member, NOT its truthiness: opencode's first
// request carries id 0.
func (r request) isNotification() bool {
	s := strings.TrimSpace(string(r.ID))
	return s == "" || s == "null"
}

// Server bridges JSON-RPC on stdio to the retrieval endpoints over HTTP.
type Server struct {
	BaseURL string
	In      io.Reader
	Out     io.Writer
	Client  *http.Client
}

// New builds a Server with sane transport defaults.
func New(baseURL string, in io.Reader, out io.Writer) *Server {
	return &Server{
		BaseURL: strings.TrimRight(baseURL, "/"),
		In:      in,
		Out:     out,
		Client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Run reads frames until stdin closes. It returns nil on clean EOF: opencode
// shutting down its side is the normal way this process ends, not an error.
func (s *Server) Run(ctx context.Context) error {
	sc := bufio.NewScanner(s.In)
	sc.Buffer(make([]byte, 64*1024), maxLine)
	w := bufio.NewWriter(s.Out)

	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			// A frame we cannot parse has no id, so there is nobody to answer.
			// Per JSON-RPC, reply with a null-id parse error and carry on.
			s.write(w, map[string]any{
				"jsonrpc": "2.0", "id": nil,
				"error": map[string]any{"code": -32700, "message": "parse error"},
			})
			continue
		}
		resp := s.dispatch(ctx, req)
		if resp != nil {
			s.write(w, resp)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("mcp-recall: read stdin: %w", err)
	}
	return nil
}

func (s *Server) write(w *bufio.Writer, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	w.Write(b)
	w.WriteByte('\n')
	w.Flush()
}

func result(id json.RawMessage, v any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": v}
}

func rpcError(id json.RawMessage, code int, msg string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg},
	}
}

func (s *Server) dispatch(ctx context.Context, req request) any {
	if req.isNotification() {
		// notifications/initialized and friends: acknowledged by silence.
		return nil
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		pv := strings.TrimSpace(p.ProtocolVersion)
		if pv == "" {
			pv = fallbackProtocolVersion
		}
		return result(req.ID, map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "recall", "version": "1"},
		})
	case "ping":
		return result(req.ID, map[string]any{})
	case "tools/list":
		return result(req.ID, map[string]any{"tools": toolDefs()})
	case "tools/call":
		return s.callTool(ctx, req)
	default:
		return rpcError(req.ID, -32601, "method not found: "+req.Method)
	}
}

func (s *Server) callTool(ctx context.Context, req request) any {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return rpcError(req.ID, -32602, "invalid params")
	}

	path, query, err := routeTool(p.Name, p.Arguments)
	if err != nil {
		return rpcError(req.ID, -32602, err.Error())
	}

	body, status, err := s.get(ctx, path, query)
	if err != nil {
		// A transport failure is reported as a tool error rather than a
		// protocol error: the agent should see "recall is unavailable" and
		// continue the conversation, not have its session torn down.
		return result(req.ID, toolFailure(fmt.Sprintf("recall server unreachable: %v", err)))
	}
	if status != http.StatusOK {
		return result(req.ID, toolFailure(strings.TrimSpace(string(body))))
	}
	return result(req.ID, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": string(body)}},
	})
}

func toolFailure(msg string) map[string]any {
	if msg == "" {
		msg = "unknown error"
	}
	return map[string]any{
		"isError": true,
		"content": []any{map[string]any{"type": "text", "text": msg}},
	}
}

func (s *Server) get(ctx context.Context, path string, q url.Values) ([]byte, int, error) {
	u := s.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxLine))
	return b, resp.StatusCode, err
}

// routeTool maps a tool call to a read-only endpoint.
//
// Only GET, and only these three paths. That is what makes "the MCP process
// never writes" a property of the code rather than a promise.
func routeTool(name string, args map[string]any) (string, url.Values, error) {
	q := url.Values{}
	str := func(k string) string {
		if v, ok := args[k].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	num := func(k string) string {
		switch v := args[k].(type) {
		case float64:
			return fmt.Sprintf("%d", int(v))
		case string:
			return strings.TrimSpace(v)
		}
		return ""
	}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}

	switch name {
	case "recall_list":
		set("from", str("from"))
		set("to", str("to"))
		set("project", str("project"))
		set("limit", num("limit"))
		set("offset", num("offset"))
		set("min_messages", num("min_messages"))
		set("notes", str("notes"))
		return "/api/recall/sessions", q, nil
	case "recall_search":
		query := str("query")
		if query == "" {
			return "", nil, fmt.Errorf("recall_search: query is required")
		}
		set("q", query)
		set("role", str("role"))
		set("from", str("from"))
		set("to", str("to"))
		set("project", str("project"))
		set("limit", num("limit"))
		set("offset", num("offset"))
		return "/api/recall/search", q, nil
	case "recall_read":
		sid := str("session_id")
		if sid == "" {
			return "", nil, fmt.Errorf("recall_read: session_id is required")
		}
		set("session_id", sid)
		set("limit", num("limit"))
		set("offset", num("offset"))
		set("order", str("order"))
		set("from", str("from"))
		set("to", str("to"))
		return "/api/recall/read", q, nil
	}
	return "", nil, fmt.Errorf("unknown tool: %s", name)
}
