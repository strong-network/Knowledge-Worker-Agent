// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package opencodeserver drives the OpenCode backend through its headless HTTP
// server (`opencode serve`) instead of spawning `opencode run` per chat turn.
//
// It has three pieces:
//
//   - client.go     — a stdlib-only HTTP + SSE client for the opencode server API
//     (create session, send prompt, respond to permissions, abort, subscribe to
//     the global /event stream).
//   - supervisor.go — a pool of `opencode serve` processes keyed by working
//     directory. opencode binds a single working directory per server process
//     (verified: a session's shell tool runs in the dir where `opencode serve`
//     was launched, and there is no per-session/per-message directory override),
//     so to preserve this app's arbitrary per-session workdirs we run one server
//     per distinct directory, started lazily and health-checked.
//   - adapter.go    — maps opencode /event bus frames (message.part.updated,
//     step-finish, tool, session.idle/error, permission.asked) into the internal
//     event shape consumed by the chat stream dispatcher.
//
// Standard library only (no third-party deps), matching the repo convention.
package opencodeserver

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

// Client talks to a single running opencode server over HTTP.
type Client struct {
	BaseURL  string // e.g. http://127.0.0.1:4096
	Username string // HTTP basic-auth username (optional)
	Password string // HTTP basic-auth password (optional)
	HTTP     *http.Client
}

// NewClient builds a Client for the given base URL. A nil-safe default HTTP
// client with no overall timeout is used (individual calls set their own
// deadlines via context; the SSE subscription must be long-lived).
func NewClient(baseURL, username, password string) *Client {
	return &Client{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		Username: username,
		Password: password,
		HTTP:     &http.Client{},
	}
}

func (c *Client) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Password != "" {
		user := c.Username
		if user == "" {
			user = "opencode"
		}
		req.SetBasicAuth(user, c.Password)
	}
	return req, nil
}

// do executes the request and, on a non-2xx response, returns an error that
// includes a snippet of the response body.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, fmt.Errorf("opencode server %s %s: HTTP %d: %s",
			req.Method, req.URL.Path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return resp, nil
}

// Health represents GET /global/health.
type Health struct {
	Healthy bool   `json:"healthy"`
	Version string `json:"version"`
}

// Health checks the server's readiness.
func (c *Client) HealthCheck(ctx context.Context) (Health, error) {
	var h Health
	req, err := c.newRequest(ctx, http.MethodGet, "/global/health", nil)
	if err != nil {
		return h, err
	}
	resp, err := c.do(req)
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return h, err
	}
	return h, nil
}

// SessionInfo is the subset of the created-session object we need.
type SessionInfo struct {
	ID string `json:"id"`
}

// CreateSession creates a new opencode session (POST /session). title may be
// empty. Returns the opencode session id (e.g. "ses_...").
func (c *Client) CreateSession(ctx context.Context, title string) (string, error) {
	body := map[string]any{}
	if title != "" {
		body["title"] = title
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/session", body)
	if err != nil {
		return "", err
	}
	resp, err := c.do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var si SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&si); err != nil {
		return "", err
	}
	if si.ID == "" {
		return "", fmt.Errorf("opencode server returned an empty session id")
	}
	return si.ID, nil
}

// SessionExists reports whether the given opencode session id still exists on
// the server (GET /session/{id}). A server restart drops all in-memory
// sessions, so a persisted id may be stale.
func (c *Client) SessionExists(ctx context.Context, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	req, err := c.newRequest(ctx, http.MethodGet, "/session/"+sessionID, nil)
	if err != nil {
		return false
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// PromptModel identifies the model for a prompt.
type PromptModel struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

// PromptRequest is the body for POST /session/{id}/prompt_async.
type PromptRequest struct {
	Model   *PromptModel     `json:"model,omitempty"`
	Agent   string           `json:"agent,omitempty"`
	System  string           `json:"system,omitempty"`
	Variant string           `json:"variant,omitempty"`
	Parts   []map[string]any `json:"parts"`
}

// PromptAsync sends a prompt without waiting for the assistant's reply
// (POST /session/{id}/prompt_async → 204). Events flow over the /event stream.
func (c *Client) PromptAsync(ctx context.Context, sessionID string, pr PromptRequest) error {
	if len(pr.Parts) == 0 {
		pr.Parts = []map[string]any{}
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/session/"+sessionID+"/prompt_async", pr)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

// TextPrompt is a convenience for sending a single text part.
func TextPart(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

// RespondPermission answers a pending permission request
// (POST /session/{id}/permissions/{permissionID}). response is one of
// "once", "always", "reject".
func (c *Client) RespondPermission(ctx context.Context, sessionID, permissionID, response string) error {
	body := map[string]any{"response": response}
	req, err := c.newRequest(ctx, http.MethodPost,
		"/session/"+sessionID+"/permissions/"+permissionID, body)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

// Abort stops a running session (POST /session/{id}/abort).
func (c *Client) Abort(ctx context.Context, sessionID string) error {
	req, err := c.newRequest(ctx, http.MethodPost, "/session/"+sessionID+"/abort", nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

// McpStatus returns the live MCP server status map from the running opencode
// server (GET /mcp). The response shape is a map of server name → object with a
// "status" field, e.g. {"github":{"status":"connected"}}. Known statuses:
// connected|connecting|disconnected|disabled|error|failed|pending.
func (c *Client) McpStatus(ctx context.Context) (map[string]string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/mcp", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw map[string]struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(raw))
	for name, v := range raw {
		out[name] = v.Status
	}
	return out, nil
}

// Skill mirrors opencode's Skill.Info (skill/index.ts). Content is the full
// SKILL.md body and Location its absolute path; both are needed to force a
// skill into a turn, and neither is ever sent to the browser.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Location    string `json:"location"`
	Content     string `json:"content"`
}

// ListSkills returns every skill opencode resolves for the given directory
// (GET /skill). Because opencode scans all of its config tiers, the result is
// the same union the model sees in <available_skills> — the user's Global plus
// the platform-owned OPENCODE_CONFIG_DIR — so no Go-side scanner is needed.
//
// directory may be empty, in which case the instance's own working directory
// is used.
func (c *Client) ListSkills(ctx context.Context, directory string) ([]Skill, error) {
	path := "/skill"
	if directory != "" {
		path += "?directory=" + url.QueryEscape(directory)
	}
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []Skill
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []Skill{}
	}
	return out, nil
}

// McpConnect asks the running opencode server to connect an MCP server by name
// (POST /mcp/{name}/connect).
func (c *Client) McpConnect(ctx context.Context, name string) error {
	return c.mcpAction(ctx, name, "connect")
}

// McpDisconnect asks the running opencode server to disconnect an MCP server by
// name (POST /mcp/{name}/disconnect).
func (c *Client) McpDisconnect(ctx context.Context, name string) error {
	return c.mcpAction(ctx, name, "disconnect")
}

func (c *Client) mcpAction(ctx context.Context, name, action string) error {
	req, err := c.newRequest(ctx, http.MethodPost, "/mcp/"+name+"/"+action, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

// ServerEvent is one decoded frame from the /event SSE stream. Envelope shape:
//
//	{ "type": "message.part.updated", "properties": { ... } }
type ServerEvent struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
}

// Subscribe opens the global SSE event stream (GET /event) and invokes fn for
// every decoded event until the context is cancelled, the stream ends, or fn
// returns false. The first frame is always "server.connected".
//
// Callers filter by sessionID inside fn (the stream is global across all
// sessions the server is running).
func (c *Client) Subscribe(ctx context.Context, fn func(ServerEvent) bool) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/event", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		// SSE frames are "data: {json}" lines separated by blank lines. The
		// opencode server emits one JSON object per data line.
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[len("data:"):])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev ServerEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		if !fn(ev) {
			return nil
		}
	}
	return scanner.Err()
}

// waitHealthy polls HealthCheck until it returns healthy or the deadline
// elapses. Used by the supervisor after starting a server process.
func (c *Client) waitHealthy(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		h, err := c.HealthCheck(hctx)
		cancel()
		if err == nil && h.Healthy {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	if lastErr != nil {
		return fmt.Errorf("server did not become healthy within %s: %w", timeout, lastErr)
	}
	return fmt.Errorf("server did not become healthy within %s", timeout)
}
