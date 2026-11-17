// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTheUnmovedPageAnswersEverything(t *testing.T) {
	h := unmovedHandler("Its chats are still in /home/developer/.copilot-web.db <&>.")
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	if rec := get("/healthz"); rec.Code != 200 || rec.Body.String() != "ok" {
		t.Errorf("/healthz: %d %q", rec.Code, rec.Body.String())
	}
	rec := get("/api/status")
	var st struct {
		Status string `json:"status"`
		Ready  bool   `json:"opencode_ready"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || st.Status != "blocked" || st.Ready {
		t.Fatalf("/api/status: %v %s", err, rec.Body.String())
	}
	if rec := get("/api/sessions"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/api/sessions: %d", rec.Code)
	}
	for _, path := range []string{"/", "/some/page"} {
		rec := get(path)
		body := rec.Body.String()
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" ||
			!strings.Contains(body, "Knowledge Worker Agent can't start") ||
			!strings.Contains(body, "/home/developer/.copilot-web.db &lt;&amp;&gt;.") || strings.Contains(body, "{{why}}") {
			t.Errorf("%s: %d, Cache-Control %q:\n%s", path, rec.Code, rec.Header().Get("Cache-Control"), body)
		}
	}
}
