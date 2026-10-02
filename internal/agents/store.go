// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package agents implements the Agent Builder's management layer: a store
// that lists, reads, writes, and deletes the user's own OpenCode agent files in
// the canonical user store (~/.config/opencode/agent/), plus the HTTP handlers
// that surface it.
//
// The store owns the file's location and format on the user's behalf: callers
// pass typed fields, the store slugifies the name into the filename and uses
// internal/agentbuilder to serialize well-formed frontmatter. Shipped/default
// agents (tracked by the defaults installer's SHA-256 manifest) are excluded
// from listing and protected from edit/delete.
package agents

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/agentbuilder"
)

// ErrNotFound is returned when an agent id does not resolve to a user agent.
var ErrNotFound = errors.New("agent not found")

// ErrProtected is returned when a mutating operation targets a shipped/default
// agent, which the builder must never edit or delete.
var ErrProtected = errors.New("agent is a shipped default and cannot be modified")

// ErrCollision is returned when a save would land on a slug already owned by a
// different agent.
type ErrCollision struct{ Slug string }

func (e ErrCollision) Error() string {
	return fmt.Sprintf("an agent named %q already exists", e.Slug)
}

// Store manages user agent files under Dir. ShippedFn reports the set of
// shipped agent filenames (base names) to exclude; it is a func so tests can
// inject a fixed set and production can wire the defaults manifest.
type Store struct {
	Dir       string
	ShippedFn func() map[string]bool
}

// Item is the list-view projection of an agent for the builder UI.
type Item struct {
	ID          string `json:"id"`   // slug / file stem
	Name        string `json:"name"` // display name (frontmatter fallback to titleized id)
	Description string `json:"description"`
	Filename    string `json:"filename"`
	Hidden      bool   `json:"hidden"`
	Disable     bool   `json:"disable"`
}

func (s *Store) shipped() map[string]bool {
	if s.ShippedFn == nil {
		return map[string]bool{}
	}
	if m := s.ShippedFn(); m != nil {
		return m
	}
	return map[string]bool{}
}

// List returns the user's own agents (shipped defaults excluded), sorted by
// display name. The slice is always non-nil.
func (s *Store) List() ([]Item, error) {
	items := []Item{}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return items, nil
		}
		return items, err
	}
	shipped := s.shipped()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") || shipped[name] {
			continue
		}
		a, err := s.read(name)
		if err != nil {
			continue // skip unreadable files rather than failing the whole list
		}
		items = append(items, toItem(a, name))
	}
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items, nil
}

// Get returns the full agent for editing. It refuses shipped defaults.
func (s *Store) Get(id string) (*agentbuilder.Agent, error) {
	filename := id + ".md"
	if s.shipped()[filename] {
		return nil, ErrProtected
	}
	a, err := s.read(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return a, nil
}

// Save writes an agent. originalID is the id being edited ("" for create); it is
// used so that renaming an agent (a) doesn't false-positive as a collision with
// itself and (b) removes the old file. Returns the resulting Item.
//
// Collision, protection, and validation are enforced here:
//   - the name must slugify to a non-empty id;
//   - the target slug must not belong to a shipped default or a different user
//     agent;
//   - the assembled agent must pass agentbuilder.Validate.
func (s *Store) Save(a *agentbuilder.Agent, originalID string) (Item, error) {
	if errs := agentbuilder.Validate(a); len(errs) > 0 {
		return Item{}, ValidationErrors(errs)
	}
	slug := agentbuilder.Slug(a.Name)
	if slug == "" {
		return Item{}, ValidationErrors([]agentbuilder.ValidationError{{
			Field: "name", Message: "Enter a name for your agent.",
		}})
	}
	filename := slug + ".md"

	if s.shipped()[filename] {
		return Item{}, ErrProtected
	}
	// Collision: the target slug exists and is not the agent being edited.
	if slug != originalID {
		if _, err := os.Stat(filepath.Join(s.Dir, filename)); err == nil {
			return Item{}, ErrCollision{Slug: slug}
		}
	}

	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Item{}, err
	}
	content := agentbuilder.Serialize(a)
	if err := os.WriteFile(filepath.Join(s.Dir, filename), []byte(content), 0o644); err != nil {
		return Item{}, err
	}

	// Rename: remove the old file once the new one is safely written.
	if originalID != "" && originalID != slug {
		oldFile := originalID + ".md"
		if !s.shipped()[oldFile] {
			_ = os.Remove(filepath.Join(s.Dir, oldFile))
		}
	}

	return toItem(a, filename), nil
}

// Export returns the raw on-disk bytes of a user agent and its filename, for a
// single-file `.md` download — exactly what OpenCode consumes. It refuses
// shipped defaults and reports ErrNotFound for unknown ids.
func (s *Store) Export(id string) (filename string, content []byte, err error) {
	filename = id + ".md"
	if s.shipped()[filename] {
		return "", nil, ErrProtected
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, filename))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, ErrNotFound
		}
		return "", nil, err
	}
	return filename, data, nil
}

// Exists reports whether a slug already resolves to an agent the store owns —
// either a shipped default or an existing user file. Used as the import
// collision check (the slug is the identity key).
func (s *Store) Exists(slug string) bool {
	filename := slug + ".md"
	if s.shipped()[filename] {
		return true
	}
	_, err := os.Stat(filepath.Join(s.Dir, filename))
	return err == nil
}

// Delete removes a user agent. It refuses shipped defaults and reports
// ErrNotFound for unknown ids.
func (s *Store) Delete(id string) error {
	filename := id + ".md"
	if s.shipped()[filename] {
		return ErrProtected
	}
	path := filepath.Join(s.Dir, filename)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return os.Remove(path)
}

// read loads and parses a single agent file by base filename.
func (s *Store) read(filename string) (*agentbuilder.Agent, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, filename))
	if err != nil {
		return nil, err
	}
	id := strings.TrimSuffix(filename, ".md")
	a, _ := agentbuilder.Parse(id, data)
	if a.Name == "" {
		a.Name = titleize(id)
	}
	return a, nil
}

func toItem(a *agentbuilder.Agent, filename string) Item {
	id := strings.TrimSuffix(filename, ".md")
	name := a.Name
	if name == "" {
		name = titleize(id)
	}
	return Item{
		ID:          id,
		Name:        name,
		Description: a.Description,
		Filename:    filename,
		Hidden:      a.Hidden,
		Disable:     a.Disable,
	}
}

// titleize converts a slug like "release-notes" into "Release Notes" for a
// display fallback when no name is derivable.
func titleize(id string) string {
	words := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '_' })
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	if len(words) == 0 {
		return id
	}
	return strings.Join(words, " ")
}

// ValidationErrors wraps a set of field-level validation problems as an error so
// handlers can detect and render them as a 422 with structured detail.
type ValidationErrors []agentbuilder.ValidationError

func (v ValidationErrors) Error() string {
	parts := make([]string, len(v))
	for i, e := range v {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "; ")
}
