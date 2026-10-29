// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_ForkSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/session/ses_parent/fork" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "ses_fork"})
	}))
	defer srv.Close()

	id, err := NewClient(srv.URL, "", "").ForkSession(context.Background(), "ses_parent")
	if err != nil || id != "ses_fork" {
		t.Fatalf("ForkSession = %q, %v", id, err)
	}
}

func TestClient_ForkSessionNeedsAnID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	if id, err := NewClient(srv.URL, "", "").ForkSession(context.Background(), "ses_parent"); err == nil {
		t.Errorf("ForkSession = %q with no error, for a reply without an id", id)
	}
}
