// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package agents

import (
	"path/filepath"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/agentbuilder"
)

// ImportPreview is the pre-adopt summary shown before an uploaded agent is
// written ("review before adopt"). It carries what the adopter needs to
// decide: identity, the capabilities the agent requests, author-declared
// dependencies, an unavailable-model warning, whether the name collides, and —
// if the file is malformed — the validation errors that block the import.
type ImportPreview struct {
	Name           string                         `json:"name"`
	Slug           string                         `json:"slug"`
	Filename       string                         `json:"filename"`
	Description    string                         `json:"description"`
	Prompt         string                         `json:"prompt"`
	Permission     *agentbuilder.Permission       `json:"permission"`
	Requires       string                         `json:"requires"`
	Model          string                         `json:"model"`
	ModelAvailable bool                           `json:"model_available"`
	Collision      bool                           `json:"collision"`
	Valid          bool                           `json:"valid"`
	Errors         []agentbuilder.ValidationError `json:"errors"`
}

// agentFromImport parses uploaded bytes into an Agent and settles its identity.
// The agent name (its OpenCode identity, and the slug/collision key) comes from
// overrideName when the importer supplied one, otherwise from the uploaded
// file's name — because OpenCode agents carry no `name` frontmatter key (the
// name is the file stem), so a raw upload's identity lives in its filename.
func agentFromImport(content []byte, filenameHint, overrideName string) *agentbuilder.Agent {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = nameFromFilename(filenameHint)
	}
	a, _ := agentbuilder.Parse(name, content)
	a.Name = name
	return a
}

// nameFromFilename turns an uploaded filename ("release-notes.md") into a
// friendly display name ("Release Notes"). Returns "" when nothing usable
// remains, which surfaces as a "name required" validation error downstream.
func nameFromFilename(filenameHint string) string {
	stem := strings.TrimSuffix(filepath.Base(filenameHint), ".md")
	return titleize(agentbuilder.Slug(stem))
}

// PreviewImport validates an uploaded agent file and assembles its pre-adopt
// preview without writing anything. models is the set of models the adopter can
// access; a named model outside that set flags ModelAvailable=false (a warning,
// not a block — per the spec, an unavailable model is still importable).
func (s *Store) PreviewImport(content []byte, filenameHint, overrideName string, models []string) ImportPreview {
	a := agentFromImport(content, filenameHint, overrideName)
	slug := agentbuilder.Slug(a.Name)

	errs := agentbuilder.Validate(a)
	p := ImportPreview{
		Name:           a.Name,
		Slug:           slug,
		Description:    a.Description,
		Prompt:         a.Prompt,
		Permission:     a.Permission,
		Requires:       agentbuilder.RequiresLine(a.Description),
		Model:          a.Model,
		ModelAvailable: a.Model == "" || modelInSet(a.Model, models),
		Valid:          len(errs) == 0,
		Errors:         errs,
	}
	if slug != "" {
		p.Filename = slug + ".md"
		p.Collision = s.Exists(slug)
	}
	return p
}

// Import validates an uploaded agent file and, if valid and non-colliding,
// writes it to the user store. It reuses Save, so validation (ValidationErrors),
// slug collision (ErrCollision — the importer resolves it by supplying
// overrideName), and shipped-default protection (ErrProtected) all apply.
func (s *Store) Import(content []byte, filenameHint, overrideName string) (Item, error) {
	a := agentFromImport(content, filenameHint, overrideName)
	return s.Save(a, "")
}

func modelInSet(model string, models []string) bool {
	for _, m := range models {
		if m == model {
			return true
		}
	}
	return false
}
