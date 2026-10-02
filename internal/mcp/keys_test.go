// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testKey = "kfp_0123456789abcdef"

func withKeyDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sds-mcp-keys")
	prev := keyDirFn
	keyDirFn = func() string { return dir }
	t.Cleanup(func() { keyDirFn = prev })
	return dir
}

const keyArtifact = `{"mcp":{"kfp":{"type":"remote","url":"https://mcp.acme.example.com/mcp","headers":{"Authorization":"Bearer {file:/x}"}}}}`
const keyMeta = `{"servers":[{"name":"kfp","auth":{"type":"api-key","label":"KFP","help":"Paste your personal API key."}}]}`

func TestCleanKeyTakesAPasteAsItComes(t *testing.T) {
	for in, want := range map[string]string{
		testKey:                        testKey,
		"  " + testKey + "\n":          testKey,
		"Bearer " + testKey:            testKey,
		"bearer   " + testKey + "\r\n": testKey,
	} {
		if got, err := cleanKey(in); err != nil || got != want {
			t.Errorf("cleanKey(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// A line break inside would let a paste add a header of its own.
	for _, in := range []string{"", "   ", "Bearer", "kfp one", testKey + "\r\nX-Evil: 1", "kfp\x00x", strings.Repeat("k", maxKeyLen+1)} {
		if _, err := cleanKey(in); err != errBadKey {
			t.Errorf("cleanKey(%q) accepted", in)
		}
	}
}

func TestKeysArePrivateAndRemovingOneKeepsTheFile(t *testing.T) {
	dir := withKeyDir(t)
	if KeySaved("kfp") {
		t.Fatal("a key before any was saved")
	}
	// The placeholder that keeps a config referencing it valid.
	if p, err := EnsureKeyFile("kfp"); err != nil {
		t.Fatal(err)
	} else if fi, err := os.Stat(p); err != nil || fi.Size() != 0 || KeySaved("kfp") {
		t.Fatalf("EnsureKeyFile on a fresh workspace: %v %v", fi, err)
	}
	if err := SetKey("KFP", "Bearer "+testKey); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "kfp")
	if b, _ := os.ReadFile(path); string(b) != testKey {
		t.Errorf("stored %q", b)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode %v, want 0600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("key dir mode %v, want 0700", fi.Mode().Perm())
	}
	if !KeySaved("kfp") {
		t.Error("KeySaved false after saving")
	}
	if p, _ := EnsureKeyFile("kfp"); p != path {
		t.Errorf("EnsureKeyFile = %q", p)
	}
	if b, _ := os.ReadFile(path); string(b) != testKey {
		t.Error("EnsureKeyFile truncated a saved key")
	}
	// A missing file would invalidate opencode's whole config, so it stays.
	if err := ClearKey("kfp"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() != 0 {
		t.Errorf("after ClearKey: %v, %v", fi, err)
	}
	if KeySaved("kfp") {
		t.Error("KeySaved true after removing")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("stray files left in the key dir: %v", entries)
	}
}

func TestKeyFileRefusesNamesThatLeaveTheDirectory(t *testing.T) {
	withKeyDir(t)
	for _, name := range []string{"", "../kfp", "a/b", ".hidden", "kfp\x00"} {
		if _, err := KeyFile(name); err == nil {
			t.Errorf("KeyFile(%q) accepted", name)
		}
	}
}

func keyRequest(t *testing.T, method, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/mcp/servers/"+name+"/key", strings.NewReader(body))
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	if method == http.MethodPut {
		HandleSetKey(rec, req)
	} else {
		HandleRemoveKey(rec, req)
	}
	return rec
}

// Connector API keys: a provisioned "api-key" server takes the user's key through the API,
// and the key never comes back out of it.
func TestKeyRoutesStoreTheKeyAndNeverReturnIt(t *testing.T) {
	withGlobalConfig(t, "")
	withPlatformConfig(t, keyArtifact, keyMeta)
	withKeyDir(t)
	changes := 0
	SetConfigChangeHook(func() { changes++ })
	t.Cleanup(func() { SetConfigChangeHook(nil) })

	sv := findServer(t, "kfp")
	if sv.Auth != AuthAPIKey || sv.KeySaved {
		t.Fatalf("before a key: auth=%q key_saved=%v", sv.Auth, sv.KeySaved)
	}
	rec := keyRequest(t, http.MethodPut, "kfp", `{"key":"`+testKey+`"}`)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), testKey) || !strings.Contains(rec.Body.String(), `"key_saved":true`) {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body.String())
	}
	if changes != 1 {
		t.Errorf("change hook ran %d times", changes)
	}
	list := httptest.NewRecorder()
	HandleList(list, httptest.NewRequest(http.MethodGet, "/api/mcp/servers", nil))
	if strings.Contains(list.Body.String(), testKey) {
		t.Error("the key reached the server list")
	}
	if !findServer(t, "kfp").KeySaved {
		t.Error("key_saved false after saving")
	}

	if rec := keyRequest(t, http.MethodPut, "kfp", `{"key":"two words"}`); rec.Code != 400 || changes != 1 {
		t.Errorf("bad key = %d, hook runs %d", rec.Code, changes)
	}
	if !KeySaved("kfp") {
		t.Error("a refused paste removed the saved key")
	}

	if rec := keyRequest(t, http.MethodDelete, "kfp", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"key_saved":false`) {
		t.Errorf("DELETE = %d %s", rec.Code, rec.Body.String())
	}
	if findServer(t, "kfp").KeySaved || changes != 2 {
		t.Errorf("after DELETE: key_saved=%v, hook runs %d", findServer(t, "kfp").KeySaved, changes)
	}
}

func TestKeyRoutesRefuseServersThatTakeNoKey(t *testing.T) {
	withGlobalConfig(t, `{"mcp":{"mine":{"type":"remote","url":"https://mine.example.com/mcp"}}}`)
	withPlatformConfig(t, atlassianArtifact, atlassianMeta)
	withKeyDir(t)
	for name, want := range map[string]int{"atlassian": 409, "mine": 409, "nosuch": 404} {
		if rec := keyRequest(t, http.MethodPut, name, `{"key":"`+testKey+`"}`); rec.Code != want {
			t.Errorf("PUT key for %s = %d, want %d", name, rec.Code, want)
		}
	}
	if KeySaved("atlassian") || KeySaved("mine") {
		t.Error("a key was stored for a server that takes none")
	}
}
