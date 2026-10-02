// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file holds the GENERIC half of opencode's credential store: reading and
// writing an API-key credential for an arbitrary provider id.
//
// It lives in this package, next to the GitHub Copilot device-flow code, for
// one reason: opencodeAuthPath() is here. Two writers to auth.json resolving
// its location independently is exactly the kind of drift that produces a
// credential the other side cannot see, so the path logic stays in one place.
//
// opencode's store is a flat map keyed by provider id, and it accepts an entry
// for a provider it has no built-in knowledge of — verified against the real
// binary. That is what makes a config-declared provider authenticatable at all.

// SetAPIKey stores an API-key credential for a provider id, preserving every
// other provider's entry. The file is written atomically at 0600.
func SetAPIKey(provider, key string) error {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return fmt.Errorf("set api key: empty provider id")
	}
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("set api key: empty key")
	}
	creds, err := readCredentials()
	if err != nil {
		return err
	}
	creds[provider] = map[string]any{"type": "api", "key": key}
	return writeCredentials(creds)
}

// RemoveAPIKey deletes a provider's stored credential. Removing one that is not
// there is not an error — the caller's intent (no credential for this provider)
// is already satisfied.
func RemoveAPIKey(provider string) error {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return fmt.Errorf("remove api key: empty provider id")
	}
	creds, err := readCredentials()
	if err != nil {
		return err
	}
	if _, ok := creds[provider]; !ok {
		return nil
	}
	delete(creds, provider)
	return writeCredentials(creds)
}

// HasCredential reports whether opencode holds any stored credential for a
// provider id. Used by the provider registry to decide whether a user-key
// provider is authenticated.
//
// This reads the credential file directly rather than shelling out to
// `opencode auth list`, because the registry is resolved on every request that
// renders the Accounts modal — a subprocess per provider per request would be
// a poor trade for information already sitting in a small local file.
func HasCredential(provider string) bool {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return false
	}
	creds, err := readCredentials()
	if err != nil {
		return false
	}
	for id, entry := range creds {
		if !strings.EqualFold(id, provider) {
			continue
		}
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		// An entry with neither a key nor an access token is a husk left by a
		// failed write; treat it as absent so the UI offers a way to fix it.
		if s, _ := m["key"].(string); strings.TrimSpace(s) != "" {
			return true
		}
		if s, _ := m["access"].(string); strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

// readCredentials loads the credential store. A missing store is an empty one.
// A corrupt store is an error rather than a silent reset: overwriting it would
// destroy the user's other credentials, including their Copilot login.
func readCredentials() (map[string]any, error) {
	raw, err := os.ReadFile(opencodeAuthPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("read credential store: %w", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}, nil
	}
	creds := map[string]any{}
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("parse credential store: %w", err)
	}
	return creds, nil
}

// writeCredentials writes the store atomically at 0600.
func writeCredentials(creds map[string]any) error {
	path := opencodeAuthPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
