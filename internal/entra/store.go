// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package entra

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// Record is a connector's Microsoft sign-in, which only Knowledge Worker Agent
// keeps: opencode gets the access token, never the refresh token.
type Record struct {
	Server       string    `json:"server"`
	ServerURL    string    `json:"serverUrl"`
	ClientID     string    `json:"clientId"`
	Tenant       string    `json:"tenant"`
	Scopes       []string  `json:"scopes"`
	Account      string    `json:"account,omitempty"`
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
	// Failed says why the last refresh was refused; it isn't retried until the
	// user signs in again.
	Failed string `json:"failed,omitempty"`
}

func (r *Record) app() App { return App{ClientID: r.ClientID, Tenant: r.Tenant} }

var (
	storeMu  sync.Mutex
	serverRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

func storeDir() string { return filepath.Join(layout.System(), "entra") }

func recordPath(server string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(server))
	if !serverRe.MatchString(name) || strings.Contains(name, "..") {
		return "", fmt.Errorf("connector name %q can't name a file", server)
	}
	return filepath.Join(storeDir(), name+".json"), nil
}

func loadLocked(server string) (*Record, error) {
	p, err := recordPath(server)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	return &r, nil
}

func saveLocked(r *Record) error {
	p, err := recordPath(r.Server)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".record-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func listLocked() ([]*Record, error) {
	entries, err := os.ReadDir(storeDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Record
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		r, err := loadLocked(name)
		if err != nil || r == nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// Has reports whether Knowledge Worker Agent keeps a Microsoft sign-in for the
// connector called server.
func Has(server string) bool {
	_, _, ok := Status(server)
	return ok
}

// Status returns the account of the connector's Microsoft sign-in, and why its
// last refresh was refused, if it was; ok is false when there is none.
func Status(server string) (account, failed string, ok bool) {
	storeMu.Lock()
	defer storeMu.Unlock()
	r, err := loadLocked(server)
	if err != nil || r == nil {
		return "", "", false
	}
	return r.Account, r.Failed, true
}

// Forget removes the connector's Microsoft sign-in. Removing opencode's copy is
// the caller's part, as for any connector.
func Forget(server string) error {
	p, err := recordPath(server)
	if err != nil {
		return err
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
