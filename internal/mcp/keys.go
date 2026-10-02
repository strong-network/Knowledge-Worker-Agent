// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// Connector API keys: personal API keys for MCP servers.
//
// A key never goes into opencode.json. It lives in a file of its own, readable
// only by the workspace user, and the server's definition reads it with
// opencode's {file:…} substitution. That is what lets an administrator define a
// server centrally while each user brings their own key: opencode drops headers
// from the user's Global when the platform config defines the server (only
// "enabled" merges through), but a file reference in the platform header is
// resolved on the user's side.
//
// opencode rejects its whole configuration when a referenced file is missing,
// which would break every chat in the workspace, not just this server. So a key
// file, once referenced, always exists: it is created empty before anything
// references it, and removing a key empties it rather than deleting it.

// keyDirFn resolves the directory holding the key files. Overridable for tests.
var keyDirFn = defaultKeyDir

// defaultKeyDir is a sibling of the user's Global opencode dir, like the
// platform dir the materializer writes, until the one-folder migration moves it.
func defaultKeyDir() string {
	if layout.Migrated() {
		return layout.MCPKeys()
	}
	return filepath.Join(filepath.Dir(filepath.Dir(opencodeConfigPath())), "sds-mcp-keys")
}

// maxKeyLen bounds what a paste can put into a request header.
const maxKeyLen = 4096

var keyNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// KeyFile returns the path of a server's key file.
func KeyFile(name string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if !keyNameRe.MatchString(n) {
		return "", fmt.Errorf("%q cannot hold an API key: use letters, digits, dot, dash or underscore", name)
	}
	return filepath.Join(keyDirFn(), n), nil
}

// EnsureKeyFile returns a server's key file, creating it empty when missing, so
// a configuration that references it is always valid.
func EnsureKeyFile(name string) (string, error) {
	path, err := KeyFile(name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		return path, f.Close()
	}
	if errors.Is(err, os.ErrExist) {
		return path, nil
	}
	return "", err
}

// KeySaved reports whether a server has a key stored.
func KeySaved(name string) bool {
	path, err := KeyFile(name)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// errBadKey is the answer for a paste that cannot be a key.
var errBadKey = errors.New("That doesn't look like an API key. Paste the key on its own, without spaces or line breaks.")

// cleanKey accepts a key as a person pastes it: surrounding whitespace is
// trimmed and a leading "Bearer " dropped, since the header adds it. Anything
// with inner whitespace or control characters is refused — a line break would
// let a paste add headers of its own.
func cleanKey(raw string) (string, error) {
	k := strings.TrimSpace(raw)
	if len(k) > 7 && strings.EqualFold(k[:7], "bearer ") {
		k = strings.TrimSpace(k[7:])
	}
	if k == "" || len(k) > maxKeyLen || strings.EqualFold(k, "bearer") {
		return "", errBadKey
	}
	for _, r := range k {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", errBadKey
		}
	}
	return k, nil
}

// SetKey stores a server's key, replacing the file atomically so opencode never
// reads half of one.
func SetKey(name, raw string) error {
	key, err := cleanKey(raw)
	if err != nil {
		return err
	}
	path, err := EnsureKeyFile(name)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".key-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(key); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ClearKey forgets a server's key. The file is emptied, never deleted: a
// missing file would invalidate the whole opencode configuration.
func ClearKey(name string) error {
	path, err := EnsureKeyFile(name)
	if err != nil {
		return err
	}
	return os.WriteFile(path, nil, 0o600)
}

// onConfigChange runs after a server is added, edited or removed, or its key
// changes. A running `opencode serve` reads its config only at startup.
var onConfigChange func()

// SetConfigChangeHook installs the function run after a server's config changes.
func SetConfigChangeHook(fn func()) { onConfigChange = fn }

func configChanged() {
	if onConfigChange != nil {
		onConfigChange()
	}
}

// keyServer resolves the server a key request is for, answering the request
// itself when there is no such server or it takes no key.
func keyServer(w http.ResponseWriter, r *http.Request) (serverView, bool) {
	sv, ok, err := mergedServer(r.PathValue("name"))
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "Failed to read MCP config: "+err.Error())
		return serverView{}, false
	}
	if !ok {
		writeJSONErr(w, http.StatusNotFound, "server not found: "+r.PathValue("name"))
		return serverView{}, false
	}
	if sv.Auth != AuthAPIKey {
		writeJSONErr(w, http.StatusConflict, sv.Name+" doesn't take an API key.")
		return serverView{}, false
	}
	return sv, true
}

// HandleSetKey serves PUT /api/mcp/servers/{name}/key. The key is never echoed
// or logged.
func HandleSetKey(w http.ResponseWriter, r *http.Request) {
	sv, ok := keyServer(w, r)
	if !ok {
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if err := SetKey(sv.Name, body.Key); err != nil {
		if errors.Is(err, errBadKey) {
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Printf("[mcp] save key for %s: %v", sv.Name, err)
		writeJSONErr(w, http.StatusInternalServerError, "The key could not be saved.")
		return
	}
	log.Printf("[mcp] API key saved for %s", sv.Name)
	configChanged()
	writeJSON(w, http.StatusOK, map[string]any{"name": sv.Name, "key_saved": true})
}

// HandleRemoveKey serves DELETE /api/mcp/servers/{name}/key.
func HandleRemoveKey(w http.ResponseWriter, r *http.Request) {
	sv, ok := keyServer(w, r)
	if !ok {
		return
	}
	if err := ClearKey(sv.Name); err != nil {
		log.Printf("[mcp] remove key for %s: %v", sv.Name, err)
		writeJSONErr(w, http.StatusInternalServerError, "The key could not be removed.")
		return
	}
	log.Printf("[mcp] API key removed for %s", sv.Name)
	configChanged()
	writeJSON(w, http.StatusOK, map[string]any{"name": sv.Name, "key_saved": false})
}
