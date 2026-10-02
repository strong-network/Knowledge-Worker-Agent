// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package providers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/env/envtest"
)

// keyRecorder captures what the handlers ask the credential store to do.
type keyRecorder struct {
	set     map[string]string
	removed []string
	err     error
	hooks   int
}

func newKeyRecorder(t *testing.T) *keyRecorder {
	t.Helper()
	rec := &keyRecorder{set: map[string]string{}}
	SetKeyStore(
		func(provider, key string) error {
			if rec.err != nil {
				return rec.err
			}
			rec.set[provider] = key
			return nil
		},
		func(provider string) error {
			if rec.err != nil {
				return rec.err
			}
			rec.removed = append(rec.removed, provider)
			return nil
		},
	)
	SetKeyChangeHook(func() { rec.hooks++ })
	t.Cleanup(func() {
		SetKeyStore(nil, nil)
		SetKeyChangeHook(nil)
	})
	return rec
}

// serve routes one request through a mux carrying the provider routes, so
// r.PathValue("id") resolves the way it does in the real server.
func serve(method, target, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/providers", HandleList)
	mux.HandleFunc("POST /api/providers/{id}/key", HandleSetKey)
	mux.HandleFunc("DELETE /api/providers/{id}/key", HandleRemoveKey)

	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rdr)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestHandleListServesTheRegistry(t *testing.T) {
	envtest.Clear(t, config.EnvCentralConfig)
	withBuiltins(t, "github-copilot")

	w := serve(http.MethodGet, "/api/providers", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var reg Registry
	if err := json.Unmarshal(w.Body.Bytes(), &reg); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	if reg.Mode != ModeBuiltin || len(reg.Providers) != 1 {
		t.Fatalf("got %+v", reg)
	}
	if !reg.Providers[0].Builtin {
		t.Error("builtin flag lost in transit — the UI needs it to pick the login flow")
	}
}

// TestHandleListServesResolvedPresets: the browser labels the composer chips
// from this, so if it were missing the chip would say "Default" while arming
// whatever the frontend's own Claude heuristic guessed instead.
func TestHandleListServesResolvedPresets(t *testing.T) {
	envtest.Clear(t, config.EnvCentralConfig)
	withBuiltins(t, "github-copilot")
	prev := resolvedPresetsFn
	SetResolvedPresets(func() map[string]string {
		return map[string]string{"default": "mistral/small", "thinking": "mistral/large"}
	})
	t.Cleanup(func() { SetResolvedPresets(prev) })

	w := serve(http.MethodGet, "/api/providers", "")
	var body struct {
		Presets map[string]string `json:"presets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Presets["default"] != "mistral/small" || body.Presets["thinking"] != "mistral/large" {
		t.Errorf("presets = %v", body.Presets)
	}
}

func TestHandleListPresetsAreNeverNull(t *testing.T) {
	// The frontend indexes into this object. Encoding it as null rather than {}
	// would make every preset lookup throw in built-in mode, which is the
	// common case.
	envtest.Clear(t, config.EnvCentralConfig)
	withBuiltins(t, "github-copilot")
	SetResolvedPresets(nil)

	w := serve(http.MethodGet, "/api/providers", "")
	if !strings.Contains(w.Body.String(), `"presets":{}`) {
		t.Errorf("presets not serialised as an object: %s", w.Body.String())
	}
}

func TestHandleSetKeyStoresAndRefreshes(t *testing.T) {
	t.Setenv(config.EnvCentralConfig, "1")
	withCredentials(t)
	centralDir(t, `{"provider":{"mistral":{}}}`, `{"providers":[{"id":"mistral","auth":{"type":"user-key"}}]}`)
	rec := newKeyRecorder(t)

	w := serve(http.MethodPost, "/api/providers/mistral/key", `{"key":"  sk-abc  "}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if rec.set["mistral"] != "sk-abc" {
		t.Errorf("stored %q, want the trimmed key", rec.set["mistral"])
	}
	// A newly-stored key makes that provider's models runnable immediately; if
	// the hook does not fire the picker stays empty until the next tick.
	if rec.hooks != 1 {
		t.Errorf("key-change hook fired %d times, want 1", rec.hooks)
	}
}

func TestHandleSetKeyRejectsUnknownProvider(t *testing.T) {
	// The id is matched against the registry rather than trusted, so a request
	// naming an arbitrary provider cannot inject an entry into the credential
	// store.
	t.Setenv(config.EnvCentralConfig, "1")
	withCredentials(t)
	centralDir(t, `{"provider":{"mistral":{}}}`, "")
	rec := newKeyRecorder(t)

	w := serve(http.MethodPost, "/api/providers/evil/key", `{"key":"sk-abc"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if len(rec.set) != 0 {
		t.Errorf("credential written for an unknown provider: %v", rec.set)
	}
}

func TestHandleSetKeyRejectsBuiltinAndManaged(t *testing.T) {
	t.Run("builtin", func(t *testing.T) {
		// Copilot and Vertex have their own sign-in flows; a pasted key would
		// be written into the store and never used.
		envtest.Clear(t, config.EnvCentralConfig)
		withBuiltins(t, "github-copilot")
		rec := newKeyRecorder(t)

		w := serve(http.MethodPost, "/api/providers/github-copilot/key", `{"key":"sk-abc"}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
		if len(rec.set) != 0 {
			t.Errorf("credential written for a built-in: %v", rec.set)
		}
	})

	t.Run("managed", func(t *testing.T) {
		// A managed provider's key belongs to the platform. Letting the user
		// paste one would silently shadow the provisioned secret.
		t.Setenv(config.EnvCentralConfig, "1")
		withCredentials(t)
		centralDir(t, `{"provider":{"corp":{}}}`,
			`{"providers":[{"id":"corp","auth":{"type":"managed","secretRef":"CORP_KEY"}}]}`)
		rec := newKeyRecorder(t)

		w := serve(http.MethodPost, "/api/providers/corp/key", `{"key":"sk-abc"}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
		if len(rec.set) != 0 {
			t.Errorf("credential written for a managed provider: %v", rec.set)
		}

		if w := serve(http.MethodDelete, "/api/providers/corp/key", ""); w.Code != http.StatusBadRequest {
			t.Fatalf("delete status = %d, want 400", w.Code)
		}
		if len(rec.removed) != 0 {
			t.Errorf("credential removed for a managed provider: %v", rec.removed)
		}
	})
}

func TestHandleSetKeyRejectsEmptyOrInvalidBody(t *testing.T) {
	t.Setenv(config.EnvCentralConfig, "1")
	withCredentials(t)
	centralDir(t, `{"provider":{"mistral":{}}}`, "")
	rec := newKeyRecorder(t)

	for _, body := range []string{`{"key":""}`, `{"key":"   "}`, `{}`, `not json`, ``} {
		w := serve(http.MethodPost, "/api/providers/mistral/key", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, w.Code)
		}
	}
	if len(rec.set) != 0 {
		t.Errorf("credential written: %v", rec.set)
	}
	if rec.hooks != 0 {
		t.Errorf("key-change hook fired on a rejected request")
	}
}

func TestHandleSetKeyDoesNotEchoStoreErrors(t *testing.T) {
	// Credential-store errors can quote the content being written, so the
	// message must be generic.
	t.Setenv(config.EnvCentralConfig, "1")
	withCredentials(t)
	centralDir(t, `{"provider":{"mistral":{}}}`, "")
	rec := newKeyRecorder(t)
	rec.err = errContaining("sk-super-secret")

	w := serve(http.MethodPost, "/api/providers/mistral/key", `{"key":"sk-super-secret"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), "sk-super-secret") {
		t.Errorf("response leaked the key: %s", w.Body.String())
	}
}

type errContaining string

func (e errContaining) Error() string { return "write failed: " + string(e) }

func TestHandleRemoveKey(t *testing.T) {
	t.Setenv(config.EnvCentralConfig, "1")
	withCredentials(t, "mistral")
	centralDir(t, `{"provider":{"mistral":{}}}`, "")
	rec := newKeyRecorder(t)

	w := serve(http.MethodDelete, "/api/providers/mistral/key", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if len(rec.removed) != 1 || rec.removed[0] != "mistral" {
		t.Errorf("removed %v, want [mistral]", rec.removed)
	}
	if rec.hooks != 1 {
		t.Errorf("key-change hook fired %d times, want 1", rec.hooks)
	}
}

func TestKeyEndpointsFailClosedWithoutAStore(t *testing.T) {
	t.Setenv(config.EnvCentralConfig, "1")
	withCredentials(t)
	centralDir(t, `{"provider":{"mistral":{}}}`, "")
	SetKeyStore(nil, nil)

	if w := serve(http.MethodPost, "/api/providers/mistral/key", `{"key":"k"}`); w.Code != http.StatusInternalServerError {
		t.Errorf("set status = %d, want 500", w.Code)
	}
	if w := serve(http.MethodDelete, "/api/providers/mistral/key", ""); w.Code != http.StatusInternalServerError {
		t.Errorf("remove status = %d, want 500", w.Code)
	}
}
