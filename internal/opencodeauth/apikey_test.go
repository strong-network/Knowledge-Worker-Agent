// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// tempStore points opencodeAuthPath() at a throwaway location. The real store
// holds the developer's live Copilot login; a test must never touch it.
func tempStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	return filepath.Join(dir, "opencode", "auth.json")
}

func readStore(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse store: %v", err)
	}
	return out
}

func TestSetAPIKeyCreatesStore(t *testing.T) {
	path := tempStore(t)

	if err := SetAPIKey("mistral", "sk-abc"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	entry, ok := readStore(t, path)["mistral"].(map[string]any)
	if !ok {
		t.Fatalf("no mistral entry in %v", readStore(t, path))
	}
	if entry["type"] != "api" || entry["key"] != "sk-abc" {
		t.Errorf("entry = %v, want an api credential", entry)
	}
	if !HasCredential("mistral") {
		t.Error("HasCredential = false right after storing a key")
	}
}

func TestSetAPIKeyPreservesOtherProviders(t *testing.T) {
	// The store is shared with the Copilot device-flow login. Clobbering it
	// would sign the user out of the assistant they are mid-conversation with.
	path := tempStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := `{"github-copilot":{"type":"oauth","access":"tok","refresh":"r"}}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SetAPIKey("mistral", "sk-abc"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	store := readStore(t, path)
	copilot, ok := store["github-copilot"].(map[string]any)
	if !ok || copilot["access"] != "tok" || copilot["refresh"] != "r" {
		t.Fatalf("copilot credential damaged: %v", store["github-copilot"])
	}
	if _, ok := store["mistral"]; !ok {
		t.Error("mistral entry missing")
	}
}

func TestCredentialStoreIsPrivate(t *testing.T) {
	path := tempStore(t)
	if err := SetAPIKey("mistral", "sk-abc"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("store mode = %#o, want 0600", perm)
	}
	// The temp file used for the atomic write must not be left behind.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temporary credential file left on disk")
	}
}

func TestSetAPIKeyRejectsEmptyInput(t *testing.T) {
	tempStore(t)
	if err := SetAPIKey("", "sk-abc"); err == nil {
		t.Error("empty provider accepted")
	}
	if err := SetAPIKey("mistral", "   "); err == nil {
		t.Error("blank key accepted")
	}
}

func TestRemoveAPIKey(t *testing.T) {
	path := tempStore(t)
	if err := SetAPIKey("mistral", "sk-abc"); err != nil {
		t.Fatal(err)
	}
	if err := SetAPIKey("acme", "sk-xyz"); err != nil {
		t.Fatal(err)
	}

	if err := RemoveAPIKey("mistral"); err != nil {
		t.Fatalf("RemoveAPIKey: %v", err)
	}
	store := readStore(t, path)
	if _, ok := store["mistral"]; ok {
		t.Error("mistral entry still present")
	}
	if _, ok := store["acme"]; !ok {
		t.Error("acme entry removed too")
	}
	if HasCredential("mistral") {
		t.Error("HasCredential = true after removal")
	}

	// Removing one that is not there already satisfies the caller's intent.
	if err := RemoveAPIKey("mistral"); err != nil {
		t.Errorf("removing an absent credential returned %v, want nil", err)
	}
	if err := RemoveAPIKey(""); err == nil {
		t.Error("empty provider accepted")
	}
}

func TestHasCredential(t *testing.T) {
	path := tempStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := `{
		"withKey":    {"type":"api","key":"sk-abc"},
		"withAccess": {"type":"oauth","access":"tok"},
		"husk":       {"type":"api"},
		"notAnObject": "nope"
	}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := map[string]bool{
		"withKey":     true,
		"withAccess":  true,
		"WITHKEY":     true, // ids are matched case-insensitively
		"husk":        false,
		"notAnObject": false,
		"absent":      false,
		"":            false,
	}
	for id, want := range cases {
		if got := HasCredential(id); got != want {
			t.Errorf("HasCredential(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestHasCredentialWithNoStore(t *testing.T) {
	tempStore(t)
	if HasCredential("mistral") {
		t.Error("HasCredential = true with no store on disk")
	}
}

func TestCorruptStoreIsNotSilentlyReset(t *testing.T) {
	// Overwriting an unreadable store would destroy every other credential in
	// it — including the Copilot login — to satisfy one key write. Fail instead.
	path := tempStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"github-copilot": {truncated`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SetAPIKey("mistral", "sk-abc"); err == nil {
		t.Fatal("SetAPIKey succeeded against a corrupt store")
	}
	if err := RemoveAPIKey("mistral"); err == nil {
		t.Fatal("RemoveAPIKey succeeded against a corrupt store")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"github-copilot": {truncated` {
		t.Errorf("corrupt store was rewritten: %s", raw)
	}
	if HasCredential("github-copilot") {
		t.Error("HasCredential = true against an unreadable store")
	}
}

func TestEmptyStoreFileIsTreatedAsEmpty(t *testing.T) {
	// A zero-byte auth.json is what an interrupted write leaves behind. It
	// carries no credentials to lose, so it must not block a key write.
	path := tempStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetAPIKey("mistral", "sk-abc"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	if !HasCredential("mistral") {
		t.Error("HasCredential = false after writing over an empty store")
	}
}
