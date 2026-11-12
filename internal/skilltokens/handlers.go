// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package skilltokens

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Skill is a skill the agent can use and the SKILL.md it comes from.
type Skill struct {
	Name string
	Path string
}

var (
	skillsMu   sync.RWMutex
	listSkills func(context.Context) ([]Skill, error)
)

// SetSkills sets how the skills the agent can use are listed.
func SetSkills(fn func(context.Context) ([]Skill, error)) {
	skillsMu.Lock()
	defer skillsMu.Unlock()
	listSkills = fn
}

// Token is one token in Accounts. Its value is never sent.
type Token struct {
	Name   string   `json:"name"`
	Label  string   `json:"label"`
	Help   string   `json:"help,omitempty"`
	URL    string   `json:"url,omitempty"`
	Skills []string `json:"skills"`
	// Source is "saved", "environment" when the workspace sets the variable
	// itself, or "none".
	Source string `json:"source"`
}

// List returns the tokens the agent's skills declare, and saved tokens no
// skill declares any more, so they can be removed. checked is false when the
// skills couldn't be listed.
func List(ctx context.Context) (tokens []Token, checked bool, err error) {
	saved, err := Saved()
	if err != nil {
		return nil, false, err
	}
	byName := map[string]*Token{}
	skillsMu.RLock()
	fn := listSkills
	skillsMu.RUnlock()
	if fn != nil {
		skills, lerr := fn(ctx)
		if lerr != nil {
			log.Printf("[skill-tokens] list skills: %v", lerr)
		} else {
			checked = true
			skills = slices.Clone(skills)
			sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
			for _, s := range skills {
				d, ok := Declared(readHead(s.Path))
				if !ok {
					continue
				}
				t := byName[d.Name]
				if t == nil {
					t = &Token{Name: d.Name, Label: d.Label, Help: d.Help, URL: d.URL, Skills: []string{}}
					if t.Label == "" {
						t.Label = s.Name
					}
					byName[d.Name] = t
				}
				t.Skills = append(t.Skills, s.Name)
			}
		}
	}
	for _, name := range saved {
		if byName[name] == nil {
			byName[name] = &Token{Name: name, Label: name, Skills: []string{}}
		}
	}
	isSaved := map[string]bool{}
	for _, name := range saved {
		isSaved[name] = true
	}
	for _, t := range byName {
		switch {
		case strings.TrimSpace(os.Getenv(t.Name)) != "":
			t.Source = "environment"
		case isSaved[t.Name]:
			t.Source = "saved"
		default:
			t.Source = "none"
		}
		tokens = append(tokens, *t)
	}
	sort.Slice(tokens, func(i, j int) bool {
		if a, b := strings.ToLower(tokens[i].Label), strings.ToLower(tokens[j].Label); a != b {
			return a < b
		}
		return tokens[i].Name < tokens[j].Name
	})
	return tokens, checked, nil
}

// readHead returns the start of a file, where its front matter is.
func readHead(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 64<<10))
	return string(b)
}

const listTimeout = 20 * time.Second

// HandleList answers GET /api/skill-tokens.
func HandleList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), listTimeout)
	defer cancel()
	tokens, checked, err := List(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if tokens == nil {
		tokens = []Token{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens, "skillsChecked": checked})
}

// HandleSave answers PUT /api/skill-tokens/{name} with {"value": "..."}.
func HandleSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if err := Save(r.PathValue("name"), body.Value); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// HandleRemove answers DELETE /api/skill-tokens/{name}.
func HandleRemove(w http.ResponseWriter, r *http.Request) {
	if err := Remove(r.PathValue("name")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeError(w http.ResponseWriter, err error) {
	var invalid InvalidError
	if errors.As(err, &invalid) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": invalid.Error()})
		return
	}
	log.Printf("[skill-tokens] %v", err)
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "The token couldn't be changed. Try again."})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
