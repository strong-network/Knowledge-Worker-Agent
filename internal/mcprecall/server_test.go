// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcprecall

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runFrames feeds newline-delimited frames through a Server and returns the
// frames it wrote back.
func runFrames(t *testing.T, base string, frames ...string) []map[string]any {
	t.Helper()
	in := strings.NewReader(strings.Join(frames, "\n") + "\n")
	var out bytes.Buffer
	s := New(base, in, &out)
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var got []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("response %q is not JSON: %v", line, err)
		}
		got = append(got, m)
	}
	return got
}

// TestInitializeWithZeroID is the regression that a live opencode 1.18.30
// handshake would otherwise have caught in production.
//
// opencode's FIRST request carries id 0. A notification check written as "the
// id is falsy" -- the natural way to write it -- silently drops `initialize`,
// the client waits forever, and the server appears simply not to connect.
// Negative control: change isNotification to test truthiness and this fails.
func TestInitializeWithZeroID(t *testing.T) {
	got := runFrames(t, "http://127.0.0.1:1",
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"opencode","version":"1.18.30"}}}`)

	if len(got) != 1 {
		t.Fatalf("got %d responses to initialize (id 0), want 1", len(got))
	}
	if id, ok := got[0]["id"].(float64); !ok || id != 0 {
		t.Errorf("response id = %v, want 0", got[0]["id"])
	}
	res, ok := got[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in initialize response: %v", got[0])
	}
	if res["protocolVersion"] != "2025-11-25" {
		t.Errorf("protocolVersion = %v, want the client's own version echoed", res["protocolVersion"])
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Error("initialize did not advertise the tools capability")
	}
}

func TestInitializeWithoutProtocolVersion(t *testing.T) {
	got := runFrames(t, "http://127.0.0.1:1",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	res := got[0]["result"].(map[string]any)
	if res["protocolVersion"] != fallbackProtocolVersion {
		t.Errorf("protocolVersion = %v, want the fallback %q", res["protocolVersion"], fallbackProtocolVersion)
	}
}

// TestNotificationsAreSilent pins the other half of the id rule: a frame with
// no id must produce no reply at all. Answering a notification is a protocol
// violation that some clients treat as fatal.
func TestNotificationsAreSilent(t *testing.T) {
	got := runFrames(t, "http://127.0.0.1:1",
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":null,"method":"notifications/cancelled"}`)
	if len(got) != 0 {
		t.Fatalf("notifications produced %d responses, want 0: %v", len(got), got)
	}
}

func TestToolsListAdvertisesThreeVerbs(t *testing.T) {
	got := runFrames(t, "http://127.0.0.1:1",
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	res := got[0]["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		m := tl.(map[string]any)
		names[m["name"].(string)] = true
		if _, ok := m["inputSchema"]; !ok {
			t.Errorf("tool %v has no inputSchema", m["name"])
		}
		if d, _ := m["description"].(string); strings.TrimSpace(d) == "" {
			t.Errorf("tool %v has no description; the agent cannot know when to use it", m["name"])
		}
	}
	for _, want := range []string{"recall_list", "recall_search", "recall_read"} {
		if !names[want] {
			t.Errorf("tools/list is missing %q (got %v)", want, names)
		}
	}
}

// TestReadSchemaAdvertisesTheWayToRecentMessages is a documentation test, and
// it is here because a documentation gap was half the production bug: the
// schema described `limit` without saying it was capped, so an agent asked for
// 1000, got the oldest 200, and had no parameter that would have taken it to
// the recent end. The schema is the only thing the agent reads before choosing
// arguments, so the escape hatches have to be visible in it.
func TestReadSchemaAdvertisesTheWayToRecentMessages(t *testing.T) {
	got := runFrames(t, "http://127.0.0.1:1",
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := got[0]["result"].(map[string]any)["tools"].([]any)

	var read map[string]any
	for _, tl := range tools {
		m := tl.(map[string]any)
		if m["name"] == "recall_read" {
			read = m
		}
	}
	if read == nil {
		t.Fatal("recall_read is not advertised")
	}

	props := read["inputSchema"].(map[string]any)["properties"].(map[string]any)
	for _, p := range []string{"order", "offset", "from", "to", "limit"} {
		if _, ok := props[p]; !ok {
			t.Errorf("recall_read schema has no %q -- an agent cannot reach recent messages without it", p)
		}
	}

	desc := read["description"].(string)
	if !strings.Contains(desc, "newest") {
		t.Error("the description never mentions order=newest, so the agent will not find it")
	}
	if !strings.Contains(desc, "200") {
		t.Error("the description never states the cap, which is how the agent came to trust a partial answer")
	}

	limitDesc := props["limit"].(map[string]any)["description"].(string)
	if !strings.Contains(limitDesc, "maximum") {
		t.Errorf("limit is described as %q without saying it is a maximum; "+
			"describing 200 as merely a 'default' is what invited limit=1000", limitDesc)
	}
}

// TestEveryVerbAdvertisesItsOwnCeilingAndOffset: the ceilings differ per verb
// now, and a schema that repeats one number for all three would send the agent
// back to guessing. Each limit must state its own maximum, and every verb must
// offer the offset that makes "truncated" recoverable.
func TestEveryVerbAdvertisesItsOwnCeilingAndOffset(t *testing.T) {
	got := runFrames(t, "http://127.0.0.1:1",
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := got[0]["result"].(map[string]any)["tools"].([]any)

	schemas := map[string]map[string]any{}
	for _, tl := range tools {
		m := tl.(map[string]any)
		schemas[m["name"].(string)] = m["inputSchema"].(map[string]any)["properties"].(map[string]any)
	}

	wantCeiling := map[string]string{
		"recall_search": "1000",
		"recall_list":   "500",
		"recall_read":   "200",
	}
	for name, ceiling := range wantCeiling {
		props, ok := schemas[name]
		if !ok {
			t.Errorf("%s is not advertised", name)
			continue
		}
		if _, ok := props["offset"]; !ok {
			t.Errorf("%s has no offset, so a truncated reply is a dead end", name)
		}
		desc := props["limit"].(map[string]any)["description"].(string)
		if !strings.Contains(desc, ceiling) {
			t.Errorf("%s limit is described as %q, which never states its %s maximum", name, desc, ceiling)
		}
		if !strings.Contains(desc, "200") {
			t.Errorf("%s limit is described as %q, which never states the 200 default", name, desc)
		}
	}
}

func TestUnknownMethod(t *testing.T) {
	got := runFrames(t, "http://127.0.0.1:1",
		`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`)
	e, ok := got[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error for unknown method: %v", got[0])
	}
	if code := e["code"].(float64); code != -32601 {
		t.Errorf("code = %v, want -32601 (method not found)", code)
	}
}

func TestParseErrorDoesNotKillTheStream(t *testing.T) {
	got := runFrames(t, "http://127.0.0.1:1",
		`{not json`,
		`{"jsonrpc":"2.0","id":4,"method":"ping"}`)
	if len(got) != 2 {
		t.Fatalf("got %d responses, want 2 (parse error, then a served ping)", len(got))
	}
	if e, ok := got[0]["error"].(map[string]any); !ok || e["code"].(float64) != -32700 {
		t.Errorf("first response is not a parse error: %v", got[0])
	}
	if _, ok := got[1]["result"]; !ok {
		t.Errorf("ping after a malformed frame was not served: %v", got[1])
	}
}

// TestToolCallHitsReadOnlyEndpoints is the containment guarantee: whatever the
// agent asks for, this process only ever issues GETs against the three recall
// paths.
func TestToolCallHitsReadOnlyEndpoints(t *testing.T) {
	type call struct {
		method string
		path   string
		query  string
	}
	var seen []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, call{r.Method, r.URL.Path, r.URL.RawQuery})
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	runFrames(t, srv.URL,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall_list","arguments":{"from":"2026-01-01","limit":5}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recall_search","arguments":{"query":"pricing","role":"user"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"recall_read","arguments":{"session_id":"abc"}}}`,
	)

	if len(seen) != 3 {
		t.Fatalf("made %d HTTP calls, want 3: %+v", len(seen), seen)
	}
	for _, c := range seen {
		if c.method != http.MethodGet {
			t.Errorf("issued a %s to %s — the recall bridge must only ever GET", c.method, c.path)
		}
		if !strings.HasPrefix(c.path, "/api/recall/") {
			t.Errorf("called %s, outside the recall surface", c.path)
		}
	}
	if seen[0].path != "/api/recall/sessions" || !strings.Contains(seen[0].query, "limit=5") {
		t.Errorf("recall_list routed to %s?%s", seen[0].path, seen[0].query)
	}
	if seen[1].path != "/api/recall/search" || !strings.Contains(seen[1].query, "q=pricing") {
		t.Errorf("recall_search routed to %s?%s", seen[1].path, seen[1].query)
	}
	if seen[2].path != "/api/recall/read" || !strings.Contains(seen[2].query, "session_id=abc") {
		t.Errorf("recall_read routed to %s?%s", seen[2].path, seen[2].query)
	}
}

// TestWindowArgsReachTheEndpoint: advertising `order` and `offset` in the
// schema is worthless if the bridge drops them on the floor. The agent would
// then pass order="newest", receive the oldest messages, and have no way to
// tell -- a quieter version of the original bug.
func TestWindowArgsReachTheEndpoint(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RawQuery)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	runFrames(t, srv.URL,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall_read","arguments":`+
			`{"session_id":"abc","order":"newest","offset":200,"limit":50,"from":"2026-09-06","to":"2026-09-09"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recall_list","arguments":{"offset":200}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"recall_search","arguments":{"query":"pricing","offset":400,"limit":600}}}`,
	)

	if len(seen) != 3 {
		t.Fatalf("made %d HTTP calls, want 3: %v", len(seen), seen)
	}
	for _, want := range []string{
		"order=newest", "offset=200", "limit=50", "from=2026-09-06", "to=2026-09-09",
	} {
		if !strings.Contains(seen[0], want) {
			t.Errorf("recall_read query %q is missing %q", seen[0], want)
		}
	}
	if !strings.Contains(seen[1], "offset=200") {
		t.Errorf("recall_list query %q is missing offset=200", seen[1])
	}
	for _, want := range []string{"offset=400", "limit=600"} {
		if !strings.Contains(seen[2], want) {
			t.Errorf("recall_search query %q is missing %q", seen[2], want)
		}
	}
}

func TestToolCallMissingRequiredArgs(t *testing.T) {
	for _, frame := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall_search","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recall_read","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"recall_delete","arguments":{}}}`,
	} {
		got := runFrames(t, "http://127.0.0.1:1", frame)
		if _, ok := got[0]["error"]; !ok {
			t.Errorf("frame %s produced no error: %v", frame, got[0])
		}
	}
}

// TestUnreachableServerIsAToolError, not a protocol error: recall being down
// should read to the agent as "this tool is unavailable", not tear down the
// session.
func TestUnreachableServerIsAToolError(t *testing.T) {
	got := runFrames(t, "http://127.0.0.1:1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall_list","arguments":{}}}`)
	if _, isProtocolError := got[0]["error"]; isProtocolError {
		t.Fatalf("an unreachable backend produced a JSON-RPC error: %v", got[0])
	}
	res, ok := got[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", got[0])
	}
	if res["isError"] != true {
		t.Errorf("isError = %v, want true", res["isError"])
	}
}

// TestHTTPErrorIsForwardedAsToolError keeps the server's own explanation (e.g.
// "search index unavailable") visible to the agent instead of replacing it.
func TestHTTPErrorIsForwardedAsToolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "search index unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	got := runFrames(t, srv.URL,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall_search","arguments":{"query":"x"}}}`)
	res := got[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("isError = %v, want true", res["isError"])
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "index unavailable") {
		t.Errorf("tool error text = %q, want the server's explanation preserved", text)
	}
}

func TestIsNotification(t *testing.T) {
	cases := map[string]bool{
		`0`:       false, // opencode's first request id
		`1`:       false,
		`"abc"`:   false,
		`null`:    true,
		``:        true,
		`  null `: true,
	}
	for raw, want := range cases {
		r := request{ID: json.RawMessage(raw)}
		if got := r.isNotification(); got != want {
			t.Errorf("isNotification(id=%q) = %v, want %v", raw, got, want)
		}
	}
}

// The tool descriptions are the agent's only discovery surface -- it reads them
// before choosing arguments and never sees this repo. PR #155 existed because
// the read schema said "default 200" where it meant "maximum 200", so wording
// here is treated as behaviour.
func TestSchemasExplainNotesAndDeletedChats(t *testing.T) {
	got := runFrames(t, "http://127.0.0.1:1",
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := got[0]["result"].(map[string]any)["tools"].([]any)

	desc := map[string]string{}
	for _, tl := range tools {
		m := tl.(map[string]any)
		desc[m["name"].(string)] = m["description"].(string)
	}

	list := desc["recall_list"]
	for _, want := range []string{"notes", "notes_cover_through", "deleted"} {
		if !strings.Contains(list, want) {
			t.Errorf("recall_list description never mentions %q, so the agent will not know it exists", want)
		}
	}
	// The coverage marker is useless unless the agent is told what its absence
	// means: silence after that date is "not summarised", not "nothing happened".
	if !strings.Contains(list, "NOT covered") {
		t.Error("recall_list does not warn that activity after notes_cover_through is uncovered")
	}

	read := desc["recall_read"]
	if !strings.Contains(read, `source="deleted"`) {
		t.Error("recall_read does not tell the agent how a deleted chat looks")
	}
	if !strings.Contains(read, "no 'messages' field") {
		t.Error("recall_read does not say the messages field is absent for a deleted chat")
	}
}
