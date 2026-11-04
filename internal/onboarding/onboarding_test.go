// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package onboarding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

func setup(t *testing.T) *http.ServeMux {
	t.Helper()
	if err := db.Init(filepath.Join(t.TempDir(), "kwa.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	prev := startID
	t.Cleanup(func() { startID = prev })
	m := http.NewServeMux()
	m.HandleFunc("GET /api/onboarding", HandleGet)
	m.HandleFunc("POST /api/onboarding/guide", HandleGuide)
	m.HandleFunc("POST /api/onboarding/whats-new/later", HandleLater)
	m.HandleFunc("POST /api/onboarding/seen", HandleSeen)
	return m
}

func call(t *testing.T, m *http.ServeMux, method, path, body string) (int, View) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, req)
	var v View
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatalf("%s %s: %v in %s", method, path, err, rec.Body)
		}
	}
	return rec.Code, v
}

// A fresh workspace has seen nothing, and the lists are empty rather than null.
func TestStartsEmpty(t *testing.T) {
	m := setup(t)
	code, v := call(t, m, "GET", "/api/onboarding", "")
	if code != http.StatusOK || v.GuideHidden || v.GuideClosed || v.WhatsNewLater ||
		v.SeenConnectors == nil || v.SeenProviders == nil || len(v.SeenConnectors)+len(v.SeenProviders) != 0 {
		t.Fatalf("got %d %+v", code, v)
	}
}

// Closing the guide, and Remind me later, last until the next start; hiding
// the guide lasts for good.
func TestClosedAndLaterLastUntilTheNextStart(t *testing.T) {
	m := setup(t)
	if _, v := call(t, m, "POST", "/api/onboarding/guide", `{"hide":false}`); !v.GuideClosed || v.GuideHidden {
		t.Fatalf("after Close: %+v", v)
	}
	if _, v := call(t, m, "POST", "/api/onboarding/whats-new/later", ""); !v.WhatsNewLater || !v.GuideClosed {
		t.Fatalf("after Remind me later: %+v", v)
	}

	startID = newStartID()
	if _, v := call(t, m, "GET", "/api/onboarding", ""); v.GuideClosed || v.WhatsNewLater || v.GuideHidden {
		t.Fatalf("after a restart: %+v", v)
	}

	if _, v := call(t, m, "POST", "/api/onboarding/guide", `{"hide":true}`); !v.GuideHidden {
		t.Fatalf("after Don't show this again: %+v", v)
	}
	startID = newStartID()
	if _, v := call(t, m, "GET", "/api/onboarding", ""); !v.GuideHidden || v.GuideClosed {
		t.Fatalf("hidden guide after a restart: %+v", v)
	}
	// Closing again without the box ticked doesn't bring it back.
	if _, v := call(t, m, "POST", "/api/onboarding/guide", `{}`); !v.GuideHidden {
		t.Fatalf("a later Close unhid the guide: %+v", v)
	}
}

// Seen names add up; connectors compare in lower case, providers as written.
func TestSeenNamesAddUp(t *testing.T) {
	m := setup(t)
	call(t, m, "POST", "/api/onboarding/seen", `{"connectors":["Jira"," figma ",""],"providers":["Acme"]}`)
	_, v := call(t, m, "POST", "/api/onboarding/seen", `{"connectors":["jira","sentry"],"providers":["acme"]}`)
	if got := strings.Join(v.SeenConnectors, ","); got != "jira,figma,sentry" {
		t.Errorf("connectors = %s", got)
	}
	if got := strings.Join(v.SeenProviders, ","); got != "Acme,acme" {
		t.Errorf("providers = %s", got)
	}
	startID = newStartID()
	if _, v := call(t, m, "GET", "/api/onboarding", ""); len(v.SeenConnectors) != 3 {
		t.Errorf("seen names lost at a restart: %+v", v)
	}
}

// Overlong names are ignored and a list keeps its newest names under the cap.
func TestSeenNamesAreCapped(t *testing.T) {
	m := setup(t)
	names := make([]string, maxNames+5)
	for i := range names {
		names[i] = fmt.Sprintf("c%d", i)
	}
	names = append(names, strings.Repeat("x", maxName+1))
	b, _ := json.Marshal(map[string]any{"connectors": names})
	_, v := call(t, m, "POST", "/api/onboarding/seen", string(b))
	if len(v.SeenConnectors) != maxNames || v.SeenConnectors[0] != "c5" || v.SeenConnectors[maxNames-1] != fmt.Sprintf("c%d", maxNames+4) {
		t.Errorf("kept %d names, first %q", len(v.SeenConnectors), v.SeenConnectors[0])
	}
}

// A body that isn't the expected JSON changes nothing.
func TestBadBodies(t *testing.T) {
	m := setup(t)
	for _, tc := range []struct{ path, body string }{
		{"/api/onboarding/guide", `{"hide":"yes"}`},
		{"/api/onboarding/guide", ``},
		{"/api/onboarding/seen", `{"connectors":"jira"}`},
		{"/api/onboarding/seen", `[`},
	} {
		if code, _ := call(t, m, "POST", tc.path, tc.body); code != http.StatusBadRequest {
			t.Errorf("POST %s %q: %d, want 400", tc.path, tc.body, code)
		}
	}
	if _, v := call(t, m, "GET", "/api/onboarding", ""); v.GuideClosed || len(v.SeenConnectors) != 0 {
		t.Errorf("a refused request changed the state: %+v", v)
	}
}

// Stored state that can't be read counts as none rather than failing.
func TestUnreadableStateCountsAsNone(t *testing.T) {
	m := setup(t)
	if err := db.SetAppMeta(metaKey, "{not json"); err != nil {
		t.Fatal(err)
	}
	if code, v := call(t, m, "GET", "/api/onboarding", ""); code != http.StatusOK || v.GuideHidden {
		t.Fatalf("got %d %+v", code, v)
	}
	if _, v := call(t, m, "POST", "/api/onboarding/seen", `{"providers":["acme"]}`); len(v.SeenProviders) != 1 {
		t.Fatalf("couldn't write over unreadable state: %+v", v)
	}
}
