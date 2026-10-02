// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package providers

import "testing"

// stub registers a built-in with a scripted auth result.
func stub(id string, ok bool) Builtin {
	return Builtin{ID: id, Label: id, AuthType: AuthOAuthDevice, CheckAuth: func() bool { return ok }}
}

func TestRegisterBuiltinIsIdempotentAndOrdered(t *testing.T) {
	ResetBuiltins()
	t.Cleanup(ResetBuiltins)

	RegisterBuiltin(stub("first", false))
	RegisterBuiltin(stub("second", true))
	// Re-registering must replace in place, not append: bootstrap can run more
	// than once, and a duplicated provider would show twice in Accounts and
	// double the auth probes.
	RegisterBuiltin(stub("first", true))

	reg := builtinRegistry(ModeBuiltin, "")
	if len(reg.Providers) != 2 {
		t.Fatalf("got %d providers, want 2: %+v", len(reg.Providers), reg.Providers)
	}
	if reg.Providers[0].ID != "first" || reg.Providers[1].ID != "second" {
		t.Errorf("registration order not preserved: %+v", reg.Providers)
	}
	if !reg.Providers[0].Authenticated {
		t.Error("re-registration did not replace the entry")
	}
	if !reg.Providers[0].Builtin {
		t.Error("built-in flag not set")
	}
}

func TestBuiltinRegistryReportsUnauthenticatedReason(t *testing.T) {
	ResetBuiltins()
	t.Cleanup(ResetBuiltins)
	RegisterBuiltin(stub("a", false))
	// A built-in with no probe at all must not be reported as signed in.
	RegisterBuiltin(Builtin{ID: "b"})

	reg := builtinRegistry(ModeBuiltin, "")
	for _, p := range reg.Providers {
		if p.Authenticated {
			t.Errorf("%s reported authenticated", p.ID)
		}
		if p.Reason != ReasonNotSignedIn {
			t.Errorf("%s reason = %q, want %q", p.ID, p.Reason, ReasonNotSignedIn)
		}
	}
}

func TestRegistryAuthenticated(t *testing.T) {
	reg := Registry{Providers: []Provider{
		{ID: "a", Authenticated: false},
		{ID: "b", Authenticated: true},
		{ID: "c", Authenticated: true},
	}}
	got := reg.Authenticated()
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Fatalf("got %v, want [b c]", got)
	}
	if !reg.AnyAuthenticated() {
		t.Error("AnyAuthenticated() = false, want true")
	}

	empty := Registry{Providers: []Provider{{ID: "a"}}}
	if empty.AnyAuthenticated() {
		t.Error("AnyAuthenticated() = true with nothing signed in")
	}
	// Never nil: the JSON encodes as [] and callers range over it directly.
	if empty.Authenticated() == nil {
		t.Error("Authenticated() returned nil, want empty slice")
	}
}

func always(string) bool { return true }

func TestRegistryPresetFirstWins(t *testing.T) {
	reg := Registry{Providers: []Provider{
		{ID: "a", Authenticated: true, Presets: map[string]string{"default": "a/one"}},
		{ID: "b", Authenticated: true, Presets: map[string]string{"default": "b/one"}},
	}}
	got, ok := reg.Preset("default", always)
	if !ok || got != "a/one" {
		t.Errorf("got (%q, %v), want (a/one, true) — assignment order must decide", got, ok)
	}
}

func TestRegistryPresetSkipsUnauthenticated(t *testing.T) {
	// A provider nobody can use must not get to dictate what Default means;
	// otherwise the composer would send a model that fails at submit time.
	reg := Registry{Providers: []Provider{
		{ID: "a", Authenticated: false, Presets: map[string]string{"default": "a/one"}},
		{ID: "b", Authenticated: true, Presets: map[string]string{"default": "b/one"}},
	}}
	if got, ok := reg.Preset("default", always); !ok || got != "b/one" {
		t.Errorf("got (%q, %v), want (b/one, true)", got, ok)
	}
}

func TestRegistryPresetSkipsUnavailableModel(t *testing.T) {
	// A nomination naming a model that was not discovered (typo, retired
	// model) degrades to the next provider instead of pinning a dead id.
	reg := Registry{Providers: []Provider{
		{ID: "a", Authenticated: true, Presets: map[string]string{"default": "a/gone"}},
		{ID: "b", Authenticated: true, Presets: map[string]string{"default": "b/one"}},
	}}
	avail := func(m string) bool { return m == "b/one" }
	if got, ok := reg.Preset("default", avail); !ok || got != "b/one" {
		t.Errorf("got (%q, %v), want (b/one, true)", got, ok)
	}
	// Nothing available at all: report "no nomination" so the caller falls back
	// to the built-in heuristic rather than resolving to nothing.
	if got, ok := reg.Preset("default", func(string) bool { return false }); ok || got != "" {
		t.Errorf("got (%q, %v), want (\"\", false)", got, ok)
	}
}

func TestRegistryPresetIsIndependentPerPreset(t *testing.T) {
	// A provider nominating only "default" must not suppress another's
	// "thinking" — resolution is per preset, not per provider.
	reg := Registry{Providers: []Provider{
		{ID: "a", Authenticated: true, Presets: map[string]string{"default": "a/one"}},
		{ID: "b", Authenticated: true, Presets: map[string]string{"thinking": "b/big"}},
	}}
	if got, _ := reg.Preset("default", always); got != "a/one" {
		t.Errorf("default = %q, want a/one", got)
	}
	if got, _ := reg.Preset("thinking", always); got != "b/big" {
		t.Errorf("thinking = %q, want b/big", got)
	}
}

func TestRegistryPresetIgnoresBlankNomination(t *testing.T) {
	reg := Registry{Providers: []Provider{
		{ID: "a", Authenticated: true, Presets: map[string]string{"default": "   "}},
		{ID: "b", Authenticated: true, Presets: map[string]string{"default": "b/one"}},
	}}
	if got, ok := reg.Preset("default", always); !ok || got != "b/one" {
		t.Errorf("got (%q, %v), want (b/one, true)", got, ok)
	}
	if got, ok := reg.Preset("nonesuch", always); ok || got != "" {
		t.Errorf("unknown preset returned (%q, %v)", got, ok)
	}
}

func TestCurateModelsAppliesOnlyTheOwningProvidersFilter(t *testing.T) {
	ResetBuiltins()
	t.Cleanup(ResetBuiltins)

	filtered := stub("picky", true)
	filtered.CurateModels = func(list []string) []string {
		out := []string{}
		for _, m := range list {
			if m != "picky/hidden" {
				out = append(out, m)
			}
		}
		return out
	}
	RegisterBuiltin(filtered)
	RegisterBuiltin(stub("plain", true))

	got := CurateModels("picky", []string{"picky/kept", "picky/hidden"})
	if len(got) != 1 || got[0] != "picky/kept" {
		t.Errorf("filter not applied: got %v", got)
	}

	// A built-in that declared no filter serves its vendor's list as-is.
	full := []string{"plain/a", "plain/b"}
	if got := CurateModels("plain", full); len(got) != 2 {
		t.Errorf("filterless built-in was curated: got %v", got)
	}

	// The decisive case: a config-declared provider is not in the built-in
	// registry at all. Its catalogue must survive untouched, or an admin who
	// added a provider by config would find models silently missing from the
	// picker with nothing in the config to explain it.
	declared := []string{"mistral-onprem/mistral-large", "picky/hidden"}
	if got := CurateModels("mistral-onprem", declared); len(got) != 2 {
		t.Errorf("config-declared provider was curated: got %v", got)
	}
}
