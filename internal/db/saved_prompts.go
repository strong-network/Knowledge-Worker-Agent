// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"fmt"
	"time"
)

// MaxSavedPrompts caps how many saved prompts (starter chips) a user can keep,
// so the New Chat chip row stays scannable and never crowds out the composer.
// The "Add prompt" control is hidden/disabled once this is reached.
const MaxSavedPrompts = 12

// savedPromptsSeededKey marks that the built-in starter chips have been seeded
// once. It lets a first-time user see the defaults while a user who has
// deleted every chip keeps an empty row (we never re-seed).
const savedPromptsSeededKey = "saved_prompts_seeded"

// researchCompanyUpgradedKey marks that the one-time upgrade of the "Research a
// company" chip has run. Seeding is gated behind savedPromptsSeededKey, so an
// existing install would otherwise never see a change to defaultSavedPrompts —
// the new chip would appear only for brand-new users and silently not for
// anyone who has already opened the app.
const researchCompanyUpgradedKey = "saved_prompt_research_company_v1"

// The "Research a company" chip. researchCompanyLegacyPrompt is what the first
// seed inserted (the label repeated as the prompt); it is the marker for "the
// user never edited this chip", and so for it being safe to rewrite.
const (
	researchCompanyName         = "Research a company"
	researchCompanyPrompt       = "Research the following company: "
	researchCompanyLegacyPrompt = "Research a company"
)

// SavedPrompt is a user-owned, reusable starter chip. It carries a
// display Name (the chip label), the Prompt text dropped into the composer on
// click, and an agent selection. The agent is stored as a kind
// ("default" | "thinking" | "custom") plus, for a custom agent, its AgentID
// (the opencode-resolvable agent id). There is deliberately no raw model field:
// Default/Thinking resolve to the vetted preset models and a custom agent
// carries its own model (consistent with the new chat surface hiding model names).
type SavedPrompt struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Prompt    string `json:"prompt"`
	AgentKind string `json:"agent_kind"`
	AgentID   string `json:"agent_id"`
	Position  int    `json:"position"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// defaultSavedPrompts are the built-in starter chips, seeded once for a
// first-time user. Name and prompt match the strings so the seeded row
// behaves like the original hard-coded chips.
//
// "Research a company" leads the row and is the one chip that isn't just its
// own label: it ends mid-sentence, so the composer opens waiting for a company
// name. It runs on Thinking because
// the analysis it kicks off is long-form research rather than a quick reply.
var defaultSavedPrompts = []struct {
	Name      string
	Prompt    string
	AgentKind string
}{
	{researchCompanyName, researchCompanyPrompt, "thinking"},
	{"Create a slide for me", "Create a slide for me", "default"},
	{"Summarize a document", "Summarize a document", "default"},
	{"Draft an email", "Draft an email", "default"},
	{"Turn notes into a brief", "Turn notes into a brief", "default"},
}

func metaGet(key string) (string, bool) {
	var v string
	err := DB.QueryRow(`SELECT value FROM app_meta WHERE key = ?`, key).Scan(&v)
	if err != nil {
		return "", false
	}
	return v, true
}

func metaSet(key, value string) error {
	_, err := DB.Exec(
		`INSERT INTO app_meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

// SeedDefaultSavedPromptsOnce inserts the built-in starter chips the first time
// it is called (tracked via app_meta), and is a no-op afterwards. This makes a
// brand-new user start with the defaults while a user who deleted all
// their chips keeps an empty row.
func SeedDefaultSavedPromptsOnce() error {
	if _, seeded := metaGet(savedPromptsSeededKey); seeded {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for i, p := range defaultSavedPrompts {
		id := fmt.Sprintf("seed-%d", i)
		if _, err := DB.Exec(
			`INSERT INTO saved_prompts (id, name, prompt, agent_kind, agent_id, position, created_at, updated_at)
			 VALUES (?, ?, ?, ?, '', ?, ?, ?)`,
			id, p.Name, p.Prompt, p.AgentKind, i, now, now,
		); err != nil {
			return err
		}
	}
	return metaSet(savedPromptsSeededKey, "1")
}

// UpgradeResearchCompanyPromptOnce brings an already-seeded install up to the
// current "Research a company" chip, once. Without it the chip would only ever
// reach new users: SeedDefaultSavedPromptsOnce is a no-op after its first run.
//
// It takes one of three paths, chosen so it can never produce two chips with
// the same label and never overwrites something the user wrote:
//
//   - the chip is present and still carries the old label-as-prompt → rewrite
//     it in place (prompt, Thinking agent) and move it to the front;
//   - the chip is present but the user has edited its prompt → leave it alone
//     entirely, and add nothing;
//   - the chip is gone → insert it at the front, subject to the chip cap.
//
// A user who deleted the old chip gets the new one because it does a different
// job (it asks for research) rather than being the same chip returning;
// that is the one case where this deviates from "never re-seed".
func UpgradeResearchCompanyPromptOnce() error {
	if _, done := metaGet(researchCompanyUpgradedKey); done {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)

	var id, prompt string
	err := DB.QueryRow(
		`SELECT id, prompt FROM saved_prompts WHERE name = ? ORDER BY position ASC LIMIT 1`,
		researchCompanyName,
	).Scan(&id, &prompt)

	switch {
	case err == nil && prompt == researchCompanyLegacyPrompt:
		var minPos int
		_ = DB.QueryRow(`SELECT COALESCE(MIN(position), 0) FROM saved_prompts`).Scan(&minPos)
		if _, err := DB.Exec(
			`UPDATE saved_prompts
			 SET prompt = ?, agent_kind = 'thinking', agent_id = '', position = ?, updated_at = ?
			 WHERE id = ?`,
			researchCompanyPrompt, minPos-1, now, id,
		); err != nil {
			return err
		}
	case err == nil:
		// Edited by the user: theirs wins, and adding a second chip with the
		// same label would be worse than doing nothing.
	default:
		if CountSavedPrompts() >= MaxSavedPrompts {
			break
		}
		var minPos int
		_ = DB.QueryRow(`SELECT COALESCE(MIN(position), 0) FROM saved_prompts`).Scan(&minPos)
		if _, err := DB.Exec(
			`INSERT INTO saved_prompts (id, name, prompt, agent_kind, agent_id, position, created_at, updated_at)
			 VALUES (?, ?, ?, 'thinking', '', ?, ?, ?)`,
			"seed-research-company", researchCompanyName, researchCompanyPrompt, minPos-1, now, now,
		); err != nil {
			return err
		}
	}
	return metaSet(researchCompanyUpgradedKey, "1")
}

// ListSavedPrompts returns all saved prompts in display order (position ASC,
// then creation order). Never returns nil.
func ListSavedPrompts() []SavedPrompt {
	rows, err := DB.Query(`
		SELECT id, name, prompt, agent_kind, agent_id, position, created_at, updated_at
		FROM saved_prompts
		ORDER BY position ASC, created_at ASC
	`)
	if err != nil {
		return []SavedPrompt{}
	}
	defer rows.Close()

	out := []SavedPrompt{}
	for rows.Next() {
		var p SavedPrompt
		if err := rows.Scan(
			&p.ID, &p.Name, &p.Prompt, &p.AgentKind, &p.AgentID,
			&p.Position, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// CountSavedPrompts returns how many saved prompts currently exist.
func CountSavedPrompts() int {
	var n int
	_ = DB.QueryRow(`SELECT COUNT(*) FROM saved_prompts`).Scan(&n)
	return n
}

// SavedPromptExists reports whether a saved prompt with the given id exists.
func SavedPromptExists(id string) bool {
	var n int
	err := DB.QueryRow(`SELECT COUNT(*) FROM saved_prompts WHERE id = ?`, id).Scan(&n)
	return err == nil && n > 0
}

// CreateSavedPrompt inserts a new saved prompt at the front of the row (a new
// chip appears first) by giving it a position below the current
// minimum. Returns the stored record.
func CreateSavedPrompt(id, name, prompt, agentKind, agentID string) (*SavedPrompt, error) {
	var minPos int
	// COALESCE so an empty table yields 0; new prompt then sorts before seeds.
	_ = DB.QueryRow(`SELECT COALESCE(MIN(position), 0) FROM saved_prompts`).Scan(&minPos)
	pos := minPos - 1
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := DB.Exec(
		`INSERT INTO saved_prompts (id, name, prompt, agent_kind, agent_id, position, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, name, prompt, agentKind, agentID, pos, now, now,
	); err != nil {
		return nil, err
	}
	return GetSavedPrompt(id), nil
}

// GetSavedPrompt returns one saved prompt or nil.
func GetSavedPrompt(id string) *SavedPrompt {
	row := DB.QueryRow(`
		SELECT id, name, prompt, agent_kind, agent_id, position, created_at, updated_at
		FROM saved_prompts WHERE id = ?
	`, id)
	var p SavedPrompt
	if err := row.Scan(
		&p.ID, &p.Name, &p.Prompt, &p.AgentKind, &p.AgentID,
		&p.Position, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil
	}
	return &p
}

// UpdateSavedPrompt rewrites the editable fields (name, prompt, agent) of a
// saved prompt. Position is unchanged. Returns the stored record.
func UpdateSavedPrompt(id, name, prompt, agentKind, agentID string) (*SavedPrompt, error) {
	_, err := DB.Exec(
		`UPDATE saved_prompts SET name = ?, prompt = ?, agent_kind = ?, agent_id = ?, updated_at = ? WHERE id = ?`,
		name, prompt, agentKind, agentID, time.Now().UTC().Format(time.RFC3339), id,
	)
	if err != nil {
		return nil, err
	}
	return GetSavedPrompt(id), nil
}

// DeleteSavedPrompt removes a saved prompt (built-in seeds delete like any
// other). Deleting the last chip does not re-seed defaults.
func DeleteSavedPrompt(id string) error {
	_, err := DB.Exec(`DELETE FROM saved_prompts WHERE id = ?`, id)
	return err
}

// ReorderSavedPrompts persists a new display order: the given ids are assigned
// ascending positions in the order provided. Ids not present are ignored; any
// existing prompt whose id is omitted keeps its old position (and thus sorts
// after the reordered ones only if its position happens to be larger). Callers
// should pass the full set of ids for a deterministic order.
func ReorderSavedPrompts(ids []string) error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for pos, id := range ids {
		if _, err := tx.Exec(
			`UPDATE saved_prompts SET position = ?, updated_at = ? WHERE id = ?`,
			pos, now, id,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}
