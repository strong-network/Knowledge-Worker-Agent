// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package skilltokens keeps the tokens skills ask the user for, such as a
// personal token for a service a skill's script calls, and gives them to
// opencode as environment variables.
//
// A skill declares its token in the metadata of its SKILL.md front matter:
//
//	metadata:
//	  kwa-token: EXAMPLE_API_TOKEN
//	  kwa-token-label: Example
//	  kwa-token-help: A personal API token from your Example profile.
//	  kwa-token-url: https://example.com/settings/tokens
//
// The user saves the token in Accounts. Nothing limits what the agent does
// with it: the agent can do whatever the token allows.
package skilltokens

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// MaxValue is the longest token accepted.
const MaxValue = 8192

var (
	nameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,62}_(TOKEN|KEY|SECRET)$`)
	// Names Knowledge Worker Agent, opencode or the GitHub account already use.
	reservedPrefixes = []string{"KWA_", "SDS_", "OPENCODE_", "COPILOT_", "GITHUB_", "GH_"}
)

// ValidName reports whether name can hold a token: an environment variable
// ending in _TOKEN, _KEY or _SECRET, so a token can never replace a variable
// that changes how programs run, such as PATH or LD_PRELOAD.
func ValidName(name string) bool {
	if !nameRe.MatchString(name) {
		return false
	}
	for _, p := range reservedPrefixes {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

type stored struct {
	Tokens map[string]string `json:"tokens"`
}

var mu sync.Mutex

func storePath() string { return filepath.Join(layout.System(), "skill-tokens.json") }

func loadLocked() (map[string]string, error) {
	b, err := os.ReadFile(storePath())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s stored
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("read %s: %w", storePath(), err)
	}
	out := make(map[string]string, len(s.Tokens))
	for k, v := range s.Tokens {
		if ValidName(k) && v != "" {
			out[k] = v
		}
	}
	return out, nil
}

func saveLocked(tokens map[string]string) error {
	p := storePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(stored{Tokens: tokens}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".skill-tokens-*")
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

// Saved returns the names of the saved tokens.
func Saved() ([]string, error) {
	mu.Lock()
	tokens, err := loadLocked()
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tokens))
	for k := range tokens {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}

// InvalidError is a token or name that can't be saved, said for the user.
type InvalidError string

func (e InvalidError) Error() string { return string(e) }

// Save stores value as the token name.
func Save(name, value string) error {
	if !ValidName(name) {
		return InvalidError(fmt.Sprintf("%s can't hold a token.", name))
	}
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return InvalidError("Enter a token.")
	case len(value) > MaxValue:
		return InvalidError(fmt.Sprintf("That token is longer than %d characters. Copy only the token.", MaxValue))
	case strings.ContainsFunc(value, unicode.IsControl):
		return InvalidError("That token has a line break in it. Copy only the token.")
	}
	mu.Lock()
	tokens, err := loadLocked()
	if err == nil {
		tokens[name] = value
		err = saveLocked(tokens)
	}
	mu.Unlock()
	if err != nil {
		return err
	}
	fireChange()
	return nil
}

// Remove deletes the token name.
func Remove(name string) error {
	mu.Lock()
	tokens, err := loadLocked()
	_, had := tokens[name]
	if err == nil && had {
		delete(tokens, name)
		err = saveLocked(tokens)
	}
	mu.Unlock()
	if err != nil {
		return err
	}
	if had {
		fireChange()
	}
	return nil
}

// Add returns env with each saved token whose variable env leaves unset or
// blank: a value the workspace sets itself wins.
func Add(env []string) []string {
	mu.Lock()
	tokens, err := loadLocked()
	mu.Unlock()
	if err != nil {
		log.Printf("[skill-tokens] %v", err)
		return env
	}
	if len(tokens) == 0 {
		return env
	}
	set := make(map[string]bool, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && v != "" {
			set[k] = true
		}
	}
	names := make([]string, 0, len(tokens))
	for k := range tokens {
		if !set[k] {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	out := slices.Clone(env)
	for _, k := range names {
		out = append(out, k+"="+tokens[k])
	}
	return out
}

var (
	changeMu sync.RWMutex
	onChange func()
)

// SetOnChange registers fn to run after a token is saved or removed.
func SetOnChange(fn func()) {
	changeMu.Lock()
	defer changeMu.Unlock()
	onChange = fn
}

func fireChange() {
	changeMu.RLock()
	fn := onChange
	changeMu.RUnlock()
	if fn != nil {
		fn()
	}
}
