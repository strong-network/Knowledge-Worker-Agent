// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

// Probe tests. The HTTP cases run against an httptest server that
// speaks (or deliberately mis-speaks) MCP; the stdio cases run tiny shell
// scripts written into t.TempDir(). Nothing here touches the network or the
// user's real config.

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func probe(t *testing.T, body map[string]any) probeResult {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/mcp/probe", strings.NewReader(string(buf)))
	w := httptest.NewRecorder()
	HandleProbe(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a failed probe is still a 200); body: %s", w.Code, w.Body.String())
	}
	var res probeResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode probe result: %v (body: %s)", err, w.Body.String())
	}
	return res
}

// mcpServer stands in for a well-behaved streamable-HTTP MCP server.
func mcpServer(t *testing.T, sse bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": probeProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "SAP Reporting", "version": "2.1"},
			}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{
				{"name": "look_up_orders"}, {"name": "read_invoices"},
			}}
		default:
			// A notification carries no id and expects no reply.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("event: message\ndata: " + string(payload) + "\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeHTTP_ReachableServerReportsNameAndTools(t *testing.T) {
	srv := mcpServer(t, false)
	res := probe(t, map[string]any{"transport": "http", "url": srv.URL})

	if !res.OK {
		t.Fatalf("ok = false, want true (error: %q)", res.Error)
	}
	if res.Name != "SAP Reporting" {
		t.Errorf("name = %q, want %q", res.Name, "SAP Reporting")
	}
	if res.Version != "2.1" {
		t.Errorf("version = %q, want %q", res.Version, "2.1")
	}
	if strings.Join(res.Tools, ",") != "look_up_orders,read_invoices" {
		t.Errorf("tools = %v", res.Tools)
	}
	if res.RequiresAuth {
		t.Error("requires_auth = true, want false")
	}
}

// Streamable-HTTP servers may answer a JSON POST with an SSE stream. The probe
// has to unwrap it, or every such server reads as "not an MCP server".
func TestProbeHTTP_UnwrapsServerSentEvents(t *testing.T) {
	srv := mcpServer(t, true)
	res := probe(t, map[string]any{"transport": "http", "url": srv.URL})

	if !res.OK {
		t.Fatalf("ok = false, want true (error: %q)", res.Error)
	}
	if res.Name != "SAP Reporting" {
		t.Errorf("name = %q, want %q", res.Name, "SAP Reporting")
	}
	if len(res.Tools) != 2 {
		t.Errorf("tools = %v, want 2", res.Tools)
	}
}

// A 401 is the design's amber "will ask you to sign in once you turn it on" —
// the address is right and the connector is worth adding.
func TestProbeHTTP_UnauthorizedIsReachableNotFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="mcp"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	res := probe(t, map[string]any{"transport": "http", "url": srv.URL})
	if !res.OK {
		t.Fatalf("ok = false, want true for a 401 (error: %q)", res.Error)
	}
	if !res.RequiresAuth {
		t.Error("requires_auth = false, want true")
	}
	if res.Error != "" {
		t.Errorf("error = %q, want empty", res.Error)
	}
}

func TestProbeHTTP_HeadersAreSent(t *testing.T) {
	seen := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case seen <- r.Header.Get("Authorization"):
		default:
		}
		json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"serverInfo": map[string]any{"name": "guarded"}},
		})
	}))
	defer srv.Close()

	res := probe(t, map[string]any{
		"transport": "http", "url": srv.URL,
		"headers": map[string]string{"Authorization": "Bearer sekrit"},
	})
	if !res.OK {
		t.Fatalf("ok = false (error: %q)", res.Error)
	}
	if got := <-seen; got != "Bearer sekrit" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer sekrit")
	}
}

// Something answering HTTP is not something speaking MCP. Reporting a web page
// as a working connector is the failure the probe exists to prevent.
func TestProbeHTTP_NonMCPResponseFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>hello</body></html>"))
	}))
	defer srv.Close()

	res := probe(t, map[string]any{"transport": "http", "url": srv.URL})
	if res.OK {
		t.Fatal("ok = true for an HTML response, want false")
	}
	if !strings.Contains(res.Error, "isn't an MCP server") {
		t.Errorf("error = %q", res.Error)
	}
}

func TestProbeHTTP_ServerErrorStatusFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	res := probe(t, map[string]any{"transport": "http", "url": srv.URL})
	if res.OK {
		t.Fatal("ok = true for HTTP 500, want false")
	}
	if !strings.Contains(res.Error, "500") {
		t.Errorf("error = %q, want it to name the status", res.Error)
	}
}

func TestProbeHTTP_RejectsNonHTTPSchemes(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "gopher://example.com/", "ftp://example.com/"} {
		res := probe(t, map[string]any{"transport": "http", "url": u})
		if res.OK {
			t.Errorf("%s: ok = true, want false", u)
		}
		if !strings.Contains(res.Error, "https://") {
			t.Errorf("%s: error = %q", u, res.Error)
		}
	}
}

func TestProbeHTTP_EmptyURLIsRejected(t *testing.T) {
	res := probe(t, map[string]any{"transport": "http", "url": "   "})
	if res.OK {
		t.Fatal("ok = true for an empty address")
	}
	if res.Error == "" {
		t.Error("want an explanatory error")
	}
}

// The cloud metadata endpoint is the one destination worth refusing: it hands
// out credentials rather than merely being reachable. The check runs on the
// resolved IP, so a hostname pointing at it is refused too.
func TestProbeHTTP_RefusesLinkLocalMetadataAddress(t *testing.T) {
	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://[fe80::1]/mcp",
	} {
		res := probe(t, map[string]any{"transport": "http", "url": u})
		if res.OK {
			t.Errorf("%s: ok = true, want refused", u)
		}
		if !strings.Contains(res.Error, "isn't allowed") {
			t.Errorf("%s: error = %q, want the refusal message", u, res.Error)
		}
	}
}

// Private and loopback addresses stay allowed on purpose: an MCP server on the
// company network is the feature's main use case. This pins that decision so a
// later "harden the probe" change has to be deliberate about breaking it.
func TestProbeHTTP_AllowsLoopbackAndPrivateAddresses(t *testing.T) {
	srv := mcpServer(t, false)
	host, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Skipf("httptest did not bind loopback (got %q)", host)
	}
	res := probe(t, map[string]any{"transport": "http", "url": "http://" + net.JoinHostPort(host, port)})
	if !res.OK {
		t.Fatalf("loopback probe refused: %q", res.Error)
	}
}

// A permitted host must not be able to bounce the probe somewhere else.
func TestProbeHTTP_DoesNotFollowRedirects(t *testing.T) {
	var target *httptest.Server
	target = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"serverInfo": map[string]any{"name": "elsewhere"}},
		})
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	res := probe(t, map[string]any{"transport": "http", "url": redirector.URL})
	if res.OK && res.Name == "elsewhere" {
		t.Fatal("probe followed the redirect")
	}
	if res.OK {
		t.Fatalf("ok = true, want false; name = %q", res.Name)
	}
}

// The reply must carry the parsed name and tool names only — never the body.
func TestProbeHTTP_DoesNotEchoResponseBody(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"credentials":"` + secret + `"}`))
	}))
	defer srv.Close()

	req := httptest.NewRequest("POST", "/api/mcp/probe",
		strings.NewReader(`{"transport":"http","url":"`+srv.URL+`"}`))
	w := httptest.NewRecorder()
	HandleProbe(w, req)

	if strings.Contains(w.Body.String(), secret) {
		t.Fatalf("probe echoed the response body: %s", w.Body.String())
	}
}

func TestProbe_MalformedJSONIsABadRequest(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/mcp/probe", strings.NewReader(`{"transport":`))
	w := httptest.NewRecorder()
	HandleProbe(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// Null-slice safety: `tools` must encode as [] so the frontend can map over it.
func TestProbe_ToolsEncodeAsArrayNotNull(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/mcp/probe", strings.NewReader(`{"transport":"http","url":""}`))
	w := httptest.NewRecorder()
	HandleProbe(w, req)
	if !strings.Contains(w.Body.String(), `"tools":[]`) {
		t.Fatalf("want tools:[] in %s", w.Body.String())
	}
}

// ── stdio ──────────────────────────────────────────────────────────────────

// stdioScript writes an executable script and returns its path.
func stdioScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stdio probe tests use a POSIX shell script")
	}
	path := filepath.Join(t.TempDir(), "fake-mcp")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProbeStdio_ReachableCommandReportsNameAndTools(t *testing.T) {
	// Reads request lines and replies in order, the newline-delimited framing
	// internal/mcprecall uses.
	script := stdioScript(t, `
while IFS= read -r line; do
  case "$line" in
    *'"initialize"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"context7","version":"9"}}}' ;;
    *'"tools/list"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"resolve_library"},{"name":"get_docs"}]}}' ;;
  esac
done
`)
	res := probe(t, map[string]any{"transport": "stdio", "command": script})
	if !res.OK {
		t.Fatalf("ok = false (error: %q)", res.Error)
	}
	if res.Name != "context7" {
		t.Errorf("name = %q, want context7", res.Name)
	}
	if strings.Join(res.Tools, ",") != "resolve_library,get_docs" {
		t.Errorf("tools = %v", res.Tools)
	}
}

// Servers log freely to stderr and sometimes print a banner to stdout. Neither
// should be mistaken for a protocol failure.
func TestProbeStdio_IgnoresNoiseOnStdoutAndStderr(t *testing.T) {
	script := stdioScript(t, `
echo "starting up..." >&2
echo "not json at all"
while IFS= read -r line; do
  case "$line" in
    *'"initialize"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"noisy"}}}' ;;
  esac
done
`)
	res := probe(t, map[string]any{"transport": "stdio", "command": script})
	if !res.OK {
		t.Fatalf("ok = false (error: %q)", res.Error)
	}
	if res.Name != "noisy" {
		t.Errorf("name = %q, want noisy", res.Name)
	}
}

// A server that answers initialize and then ignores tools/list must return as
// soon as the tools budget is up, not sit until the overall probe deadline. The
// first cut of this waited the full probeTimeout, which meant a 10-second wait
// for an answer that had been decided in milliseconds.
func TestProbeStdio_SilentToolsListDoesNotStallTheProbe(t *testing.T) {
	script := stdioScript(t, `
while IFS= read -r line; do
  case "$line" in
    *'"initialize"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"terse"}}}' ;;
  esac
done
`)
	start := time.Now()
	res := probe(t, map[string]any{"transport": "stdio", "command": script})
	elapsed := time.Since(start)

	if !res.OK {
		t.Fatalf("ok = false (error: %q)", res.Error)
	}
	if len(res.Tools) != 0 {
		t.Errorf("tools = %v, want empty", res.Tools)
	}
	if elapsed >= probeTimeout {
		t.Fatalf("probe took %v — it waited out the full probeTimeout (%v) instead of the tools budget (%v)",
			elapsed, probeTimeout, probeToolsTimeout)
	}
}

func TestProbeStdio_MissingCommandFails(t *testing.T) {
	res := probe(t, map[string]any{"transport": "stdio", "command": "definitely-not-installed-xyzzy"})
	if res.OK {
		t.Fatal("ok = true for a command that does not exist")
	}
	if !strings.Contains(res.Error, "Couldn't") {
		t.Errorf("error = %q", res.Error)
	}
}

func TestProbeStdio_EmptyCommandIsRejected(t *testing.T) {
	res := probe(t, map[string]any{"transport": "stdio", "command": "  "})
	if res.OK {
		t.Fatal("ok = true for an empty command")
	}
}

// A command that runs but says nothing is not an MCP server. This must fail
// rather than hang: the context deadline is what ends it.
func TestProbeStdio_SilentCommandFails(t *testing.T) {
	script := stdioScript(t, "exit 0\n")
	res := probe(t, map[string]any{"transport": "stdio", "command": script})
	if res.OK {
		t.Fatal("ok = true for a command that exits without speaking MCP")
	}
}

func TestProbeStdio_EnvIsPassedToTheCommand(t *testing.T) {
	script := stdioScript(t, `
while IFS= read -r line; do
  case "$line" in
    *'"initialize"'*)
      printf '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"%s"}}}\n' "$PROBE_TOKEN" ;;
  esac
done
`)
	res := probe(t, map[string]any{
		"transport": "stdio", "command": script,
		"env": map[string]string{"PROBE_TOKEN": "from-env"},
	})
	if !res.OK {
		t.Fatalf("ok = false (error: %q)", res.Error)
	}
	if res.Name != "from-env" {
		t.Errorf("name = %q, want the value passed in env", res.Name)
	}
}

// The command and its arguments arrive already split, so shell metacharacters
// in an argument are data, not syntax.
func TestProbeStdio_ArgsAreNotShellInterpreted(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pwned")
	script := stdioScript(t, `
while IFS= read -r line; do
  case "$line" in
    *'"initialize"'*)
      printf '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"%s"}}}\n' "$1" ;;
  esac
done
`)
	res := probe(t, map[string]any{
		"transport": "stdio", "command": script,
		"args": []string{"; touch " + marker},
	})
	if !res.OK {
		t.Fatalf("ok = false (error: %q)", res.Error)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("argument was interpreted by a shell")
	}
	if res.Name != "; touch "+marker {
		t.Errorf("arg = %q, want it passed through verbatim", res.Name)
	}
}

// The probe must not write config. Adding is a separate, explicit step.
func TestProbe_DoesNotPersistAnything(t *testing.T) {
	before, err := listServers()
	if err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t, false)
	if res := probe(t, map[string]any{"transport": "http", "url": srv.URL}); !res.OK {
		t.Fatalf("probe failed: %q", res.Error)
	}
	after, err := listServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("probe changed the server list: %d → %d", len(before), len(after))
	}
}
