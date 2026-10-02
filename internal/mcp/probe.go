// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

// Probe: check an MCP server before it is added.
//
// "Add connector" stays disabled until a probe succeeds, so the user never
// stores an address that was going to fail and then has to work out why from a
// row that just reads "off". The probe speaks the same MCP handshake opencode
// will speak — initialize, then tools/list — because anything less (a plain
// HEAD, a TCP connect) proves the host is up without proving there is an MCP
// server on it.
//
// The probe never mutates anything: it does not write config, and its result is
// not persisted.
//
// Security. This is the one place the server fetches a URL the browser chose,
// so it is the one place SSRF is a new risk rather than pre-existing behaviour
// (see AGENTS.md §5). Constraints, in probeHTTP and safeDialer below:
//
//   - http/https only — no file://, gopher://, etc.
//   - redirects are not followed, so a permitted host cannot bounce us to a
//     denied one.
//   - link-local addresses (169.254.0.0/16, fe80::/10) are refused at dial
//     time, after DNS resolution. That covers the cloud metadata endpoints
//     (169.254.169.254, fd00:ec2::254 — which AWS also routes via the
//     link-local v4 address) and, because the check runs on the resolved IP
//     rather than the hostname, it is not defeated by DNS rebinding.
//   - the response body is capped and never echoed back: the reply carries the
//     parsed server name and tool names only. An error string describes the
//     failure class, not the payload.
//
// Deliberately NOT blocked: private ranges (10/8, 172.16/12, 192.168/16) and
// loopback. The feature's main use case is an MCP server on the company network
// — the design's own example is https://mcp.internal.company.com/sap — and a
// workspace-local stdio server on 127.0.0.1 is a normal thing to probe. Denying
// those would break the feature to prevent an attack the user can already
// perform more directly: they can register a stdio server, which opencode then
// executes. The metadata endpoint is different in kind, because it hands out
// cloud credentials rather than merely being reachable, so it is worth the one
// rule.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const (
	// One probe must not outlive the user's patience or hold a connection open
	// indefinitely. A slow MCP server is a failed probe.
	probeTimeout = 10 * time.Second
	// Enough for a tools/list from a large server (the GitHub MCP server lists
	// ~47 tools); far short of letting a hostile endpoint stream at us.
	probeMaxBody = 1 << 20 // 1 MiB
	// tools/list is a bonus, not a gate (see below), so it gets its own much
	// smaller budget. Without this a server that answers initialize and then
	// ignores tools/list makes the user wait out the whole probeTimeout for an
	// answer that was already decided.
	probeToolsTimeout = 2 * time.Second
	// The version opencode negotiates. Echoed by well-behaved servers.
	probeProtocolVersion = "2025-11-25"
)

// probeRequest mirrors the POST /api/mcp/servers body on purpose. The frontend
// sends the same object to both endpoints, so a probe cannot succeed against a
// subtly different server than the one that gets added — in particular the
// command line arrives already split into command + args, exactly as HandleAdd
// expects, so the two paths can never disagree about how a command tokenizes.
type probeRequest struct {
	Transport string            `json:"transport"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
}

// probeResult is what the sheet renders. Note what is absent: no response body,
// no headers, no stderr. `Error` is a sentence for the user, not a dump.
type probeResult struct {
	OK           bool     `json:"ok"`
	Name         string   `json:"name,omitempty"`
	Version      string   `json:"version,omitempty"`
	Tools        []string `json:"tools"`
	RequiresAuth bool     `json:"requires_auth"`
	Error        string   `json:"error,omitempty"`
}

// HandleProbe checks that an MCP server is reachable and speaks MCP, without
// storing anything. POST /api/mcp/probe
func HandleProbe(w http.ResponseWriter, r *http.Request) {
	var body probeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, probeMaxBody)).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	r.Body.Close()

	transport := strings.ToLower(strings.TrimSpace(body.Transport))
	if transport == "" {
		if strings.TrimSpace(body.URL) != "" {
			transport = "http"
		} else {
			transport = "stdio"
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()

	var res probeResult
	switch transport {
	case "stdio", "local":
		res = probeStdio(ctx, body)
	default:
		res = probeHTTP(ctx, body)
	}
	if res.Tools == nil {
		res.Tools = []string{}
	}
	// A failed probe is a successful answer to the question that was asked, so
	// the HTTP status stays 200 and `ok` carries the verdict. Only a malformed
	// request is a 4xx.
	writeJSON(w, http.StatusOK, res)
}

// ── HTTP transport ─────────────────────────────────────────────────────────

var errLinkLocal = errors.New("that address is not allowed")

// safeDialer refuses link-local destinations. Control runs after DNS
// resolution, with the concrete IP this connection will use, which is why this
// cannot be sidestepped by a hostname that resolves to metadata.
var safeDialer = &net.Dialer{
	Timeout: 5 * time.Second,
	Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("unresolvable address")
		}
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return errLinkLocal
		}
		return nil
	},
}

func newProbeClient() *http.Client {
	return &http.Client{
		Timeout: probeTimeout,
		Transport: &http.Transport{
			DialContext:           safeDialer.DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: probeTimeout,
			DisableKeepAlives:     true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func probeHTTP(ctx context.Context, req probeRequest) probeResult {
	raw := strings.TrimSpace(req.URL)
	if raw == "" {
		return probeResult{Error: "Enter a server address first."}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return probeResult{Error: "That doesn't look like a web address. It should start with https://"}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return probeResult{Error: "The address must start with https:// (or http:// on your own network)."}
	}

	client := newProbeClient()
	session := ""

	postCtx := func(ctx context.Context, payload any, notification bool) (*http.Response, []byte, error) {
		buf, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, err
		}
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, raw, bytes.NewReader(buf))
		if err != nil {
			return nil, nil, err
		}
		hr.Header.Set("Content-Type", "application/json")
		// Streamable-HTTP MCP servers may answer either way; accept both and
		// unwrap SSE below.
		hr.Header.Set("Accept", "application/json, text/event-stream")
		hr.Header.Set("MCP-Protocol-Version", probeProtocolVersion)
		if session != "" {
			hr.Header.Set("Mcp-Session-Id", session)
		}
		for k, v := range req.Headers {
			if k = strings.TrimSpace(k); k != "" {
				hr.Header.Set(k, v)
			}
		}
		resp, err := client.Do(hr)
		if err != nil {
			return nil, nil, err
		}
		defer resp.Body.Close()
		if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
			session = id
		}
		if notification {
			io.Copy(io.Discard, io.LimitReader(resp.Body, probeMaxBody))
			return resp, nil, nil
		}
		payloadBytes, err := io.ReadAll(io.LimitReader(resp.Body, probeMaxBody))
		return resp, payloadBytes, err
	}
	post := func(payload any, notification bool) (*http.Response, []byte, error) {
		return postCtx(ctx, payload, notification)
	}

	resp, raw1, err := post(rpcRequest(1, "initialize", map[string]any{
		"protocolVersion": probeProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "knowledge-worker-agent-probe", "version": "1"},
	}), false)
	if err != nil {
		return probeResult{Error: describeDialError(err)}
	}

	// 401/403 is not a failure: the server is there and told us who it is, it
	// just wants credentials. That is the design's amber "will ask you to sign
	// in once you turn it on" — an outcome worth adding, not an error.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return probeResult{OK: true, RequiresAuth: true, Tools: []string{}}
	}
	if resp.StatusCode >= 400 {
		return probeResult{Error: fmt.Sprintf(
			"The server answered with an error (HTTP %d). Check the address, or ask whoever gave it to you whether it needs extra headers.",
			resp.StatusCode)}
	}

	init, err := decodeRPC(raw1)
	if err != nil {
		return probeResult{Error: "Something answered at that address, but it isn't an MCP server."}
	}
	if init.Error != nil {
		return probeResult{Error: "The server refused the connection: " + init.Error.Message}
	}

	res := probeResult{OK: true, Tools: []string{}}
	res.Name, res.Version = serverIdentity(init.Result)

	// Per the MCP lifecycle the client confirms initialization before issuing
	// other calls. Best-effort: a server that doesn't care will ignore it.
	post(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, true)

	// tools/list is a bonus, not a gate. A server that completed initialize is
	// reachable and speaks MCP, which is what the user is being asked to
	// confirm; failing the whole probe because the tool list was awkward would
	// block an address that actually works. It gets its own short deadline so a
	// server that never answers costs two seconds, not the rest of the probe.
	toolsCtx, cancelTools := context.WithTimeout(ctx, probeToolsTimeout)
	defer cancelTools()
	if resp, rawTools, err := postCtx(toolsCtx, rpcRequest(2, "tools/list", map[string]any{}), false); err == nil && resp.StatusCode < 400 {
		if msg, err := decodeRPC(rawTools); err == nil && msg.Error == nil {
			res.Tools = toolNames(msg.Result)
		}
	}
	return res
}

// ── stdio transport ────────────────────────────────────────────────────────

func probeStdio(ctx context.Context, req probeRequest) probeResult {
	command := strings.TrimSpace(req.Command)
	if command == "" {
		return probeResult{Error: "Enter the command to run first."}
	}

	// Arg vector, never a shell — the command and its arguments arrive already
	// split, so nothing here is interpreted.
	cmd := exec.CommandContext(ctx, command, req.Args...)
	cmd.Env = os.Environ()
	for k, v := range req.Env {
		if k = strings.TrimSpace(k); k != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return probeResult{Error: "Couldn't start that command."}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return probeResult{Error: "Couldn't start that command."}
	}
	// Discard stderr rather than collecting it: MCP servers log freely there,
	// and the user is being asked a yes/no question, not to read a log.
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return probeResult{Error: "Couldn't find “" + command + "”. Check the command is spelled correctly and installed."}
		}
		return probeResult{Error: "Couldn't run that command: " + err.Error()}
	}
	defer func() {
		stdin.Close()
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		cmd.Wait()
	}()

	enc := json.NewEncoder(stdin)
	if err := enc.Encode(rpcRequest(1, "initialize", map[string]any{
		"protocolVersion": probeProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "knowledge-worker-agent-probe", "version": "1"},
	})); err != nil {
		return probeResult{Error: "That command started but closed immediately — it doesn't look like an MCP server."}
	}

	// Newline-delimited JSON, same framing internal/mcprecall uses. Lines that
	// aren't JSON-RPC are skipped: a server that greets on stdout is unusual
	// but not broken.
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 0, 64*1024), probeMaxBody)

	init, ok := readRPCResponse(lines, 1)
	if !ok {
		return probeResult{Error: "That command ran, but it didn't answer as an MCP server."}
	}
	if init.Error != nil {
		return probeResult{Error: "The server refused the connection: " + init.Error.Message}
	}

	res := probeResult{OK: true, Tools: []string{}}
	res.Name, res.Version = serverIdentity(init.Result)

	enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if err := enc.Encode(rpcRequest(2, "tools/list", map[string]any{})); err == nil {
		if msg, ok := readRPCWithin(lines, 2, probeToolsTimeout); ok && msg.Error == nil {
			res.Tools = toolNames(msg.Result)
		}
	}
	return res
}

// ── JSON-RPC plumbing ──────────────────────────────────────────────────────

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcMessage struct {
	ID     *json.RawMessage `json:"id"`
	Result json.RawMessage  `json:"result"`
	Error  *rpcError        `json:"error"`
}

func rpcRequest(id int, method string, params any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
}

// decodeRPC parses a response body that is either a bare JSON-RPC message or an
// SSE stream carrying one. Streamable-HTTP servers choose per request, and the
// choice is not ours to make.
func decodeRPC(body []byte) (*rpcMessage, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, errors.New("empty response")
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		var msg rpcMessage
		if err := json.Unmarshal(trimmed, &msg); err == nil && (msg.Result != nil || msg.Error != nil) {
			return &msg, nil
		}
	}
	// SSE: pull the first `data:` payload that parses as a JSON-RPC response.
	sc := bufio.NewScanner(bytes.NewReader(trimmed))
	sc.Buffer(make([]byte, 0, 64*1024), probeMaxBody)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var msg rpcMessage
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if err := json.Unmarshal([]byte(payload), &msg); err == nil && (msg.Result != nil || msg.Error != nil) {
			return &msg, nil
		}
	}
	return nil, errors.New("not a JSON-RPC response")
}

// readRPCResponse scans newline-delimited output for the reply to one request
// id, skipping notifications and noise the server may interleave.
func readRPCResponse(sc *bufio.Scanner, wantID int) (*rpcMessage, bool) {
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil || msg.ID == nil {
			continue
		}
		var id int
		if err := json.Unmarshal(*msg.ID, &id); err != nil || id != wantID {
			continue
		}
		return &msg, true
	}
	return nil, false
}

// readRPCWithin is readRPCResponse with its own deadline, for a reply we would
// like but can do without. The scan itself cannot be interrupted, so it is left
// running in the background; the caller's deferred Kill closes the pipe and
// ends it. Nothing is written after the timeout, so the abandoned goroutine
// cannot affect the result.
func readRPCWithin(sc *bufio.Scanner, wantID int, d time.Duration) (*rpcMessage, bool) {
	type reply struct {
		msg *rpcMessage
		ok  bool
	}
	ch := make(chan reply, 1)
	go func() {
		msg, ok := readRPCResponse(sc, wantID)
		ch <- reply{msg, ok}
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.msg, r.ok
	case <-timer.C:
		return nil, false
	}
}

func serverIdentity(result json.RawMessage) (name, version string) {
	var r struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return "", ""
	}
	return strings.TrimSpace(r.ServerInfo.Name), strings.TrimSpace(r.ServerInfo.Version)
}

func toolNames(result json.RawMessage) []string {
	var r struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return []string{}
	}
	out := make([]string, 0, len(r.Tools))
	for _, t := range r.Tools {
		if n := strings.TrimSpace(t.Name); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// describeDialError turns a transport failure into something a non-technical
// user can act on, without leaking the response or the internal error chain.
func describeDialError(err error) string {
	if errors.Is(err, errLinkLocal) {
		return "That address isn't allowed."
	}
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return "The server didn't answer in time. It may be slow, or not reachable from here."
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "Couldn't find that address. Check for a typo in the server name."
	}
	return "Couldn't reach that address. Check for a typo, or ask whoever gave it to you whether it needs to be on the company network."
}
