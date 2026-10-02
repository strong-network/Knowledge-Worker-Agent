// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcprecall

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// min_messages was already honoured by the HTTP handler and by
// RecallListOptions; it was simply unreachable, because the bridge never sent
// it and the schema never advertised it. Both halves have to be there: a
// parameter the model cannot see is a parameter that does not exist.
func TestListArgsReachTheEndpoint(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.RawQuery
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	runFrames(t, srv.URL,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall_list","arguments":`+
			`{"from":"2026-09-01","to":"2026-09-10","min_messages":6,"notes":"none"}}}`,
	)

	for _, want := range []string{
		"from=2026-09-01", "to=2026-09-10", "min_messages=6", "notes=none",
	} {
		if !strings.Contains(seen, want) {
			t.Errorf("recall_list query %q is missing %q", seen, want)
		}
	}
}

// A parameter absent from the arguments must not be sent at all, or every call
// would carry defaults the caller never chose.
func TestUnsetListArgsAreNotSent(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.RawQuery
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	runFrames(t, srv.URL,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall_list","arguments":{"limit":10}}}`,
	)

	for _, unwanted := range []string{"min_messages", "notes"} {
		if strings.Contains(seen, unwanted) {
			t.Errorf("recall_list query %q sent %q without being asked", seen, unwanted)
		}
	}
}

// The schema is the only thing the model reads. These are the two behaviours
// an agent cannot discover by trying: that extra search words narrow rather
// than widen, and that a cheap note-free sweep exists.
func TestSchemasDocumentTheNewParameters(t *testing.T) {
	got := runFrames(t, "http://127.0.0.1:1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	res := got[0]["result"].(map[string]any)
	tools, _ := res["tools"].([]any)

	names := map[string]map[string]any{}
	for _, tl := range tools {
		m := tl.(map[string]any)
		schema := m["inputSchema"].(map[string]any)
		names[m["name"].(string)] = schema["properties"].(map[string]any)
	}

	list := names["recall_list"]
	for _, want := range []string{"min_messages", "notes"} {
		if _, ok := list[want]; !ok {
			t.Errorf("recall_list schema does not advertise %q", want)
		}
	}

	// The AND semantics are the silent trap: a longer, more natural query is
	// strictly narrower, which is the opposite of what a caller expects.
	query := names["recall_search"]["query"].(map[string]any)
	desc := query["description"].(string)
	for _, want := range []string{"ALL", "SAME message", "NARROWS"} {
		if !strings.Contains(desc, want) {
			t.Errorf("recall_search query description does not explain %q: %q", want, desc)
		}
	}
	if !strings.Contains(desc, "any") {
		t.Errorf("recall_search query description does not mention the automatic fallback: %q", desc)
	}
}

// The span field is inert unless the description says what to do about it.
// The agent's failure was not that it lacked the data -- `total` and
// `truncated` were already there -- it was that nothing told it the
// unreturned matches might be NEWER, so a page of July hits read as a
// settled answer. That instruction is the whole change; without it the
// field is decoration.
func TestSearchToolDescriptionTellsTheAgentToActOnSpan(t *testing.T) {
	var searchDesc string
	for _, def := range toolDefs() {
		m := def.(map[string]any)
		if m["name"] == "recall_search" {
			searchDesc = m["description"].(string)
		}
	}
	if searchDesc == "" {
		t.Fatal("recall_search has no description")
	}
	if !strings.Contains(searchDesc, "span") {
		t.Errorf("recall_search description never mentions span, so the agent will not look at it: %q", searchDesc)
	}
	// Naming the field is not enough: it has to say which way to look and
	// what to do next.
	for _, want := range []string{"newest", "from="} {
		if !strings.Contains(searchDesc, want) {
			t.Errorf("recall_search description does not tell the agent to %q on a later span: %q", want, searchDesc)
		}
	}
	// The threshold is "later at all", not "much later". In the case this was
	// built for the gap was two days -- 2026-08-31 in hand, 2026-09-02 out of
	// reach -- and any hedge that invites the agent to judge the size of the
	// gap first will let exactly that case through.
	if strings.Contains(searchDesc, "much later") {
		t.Errorf("recall_search description hedges the span threshold, which excuses the small gaps that caused this: %q", searchDesc)
	}
}
