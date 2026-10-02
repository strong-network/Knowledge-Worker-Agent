// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package agentbuilder

import (
	"fmt"
	"strings"
)

// ValidationError is a single, user-facing problem with an agent. Field is the
// logical field it concerns (e.g. "name", "mode", "permission.edit"); Message is
// phrased for display in the UI / import rejection toast.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e ValidationError) Error() string { return e.Field + ": " + e.Message }

// Validate checks an assembled Agent against OpenCode's agent schema. It returns
// all problems found (not just the first), so the UI can surface them together.
// A nil/empty result means the agent is valid.
//
// Because the builder controls serialization, created/edited agents virtually
// always pass; Validate earns its keep on import, converting OpenCode's silent
// skip of a malformed file into an explicit, actionable error set.
func Validate(a *Agent) []ValidationError {
	var errs []ValidationError

	if strings.TrimSpace(a.Name) == "" {
		errs = append(errs, ValidationError{"name", "Enter a name for your agent."})
	} else if Slug(a.Name) == "" {
		errs = append(errs, ValidationError{"name", "This name has no letters or numbers. Enter a name that includes at least one letter or number."})
	}

	if a.Mode != "" && !ValidMode(a.Mode) {
		errs = append(errs, ValidationError{"mode", fmt.Sprintf("%q is not a valid agent type. Use subagent, primary, or all.", string(a.Mode))})
	}

	if a.Temperature != nil && (*a.Temperature < 0 || *a.Temperature > 2) {
		errs = append(errs, ValidationError{"temperature", "Response variability must be between 0 and 2."})
	}
	if a.TopP != nil && (*a.TopP < 0 || *a.TopP > 1) {
		errs = append(errs, ValidationError{"top_p", "Response focus must be between 0 and 1."})
	}
	if a.Steps != nil && *a.Steps < 0 {
		errs = append(errs, ValidationError{"steps", "Maximum steps can't be negative."})
	}

	if !a.Permission.IsZero() {
		if a.Permission.Global != "" && !ValidAction(a.Permission.Global) {
			errs = append(errs, ValidationError{"permission", fmt.Sprintf("%q is not a valid permission. Use allow, ask, or deny.", string(a.Permission.Global))})
		}
		for cap, act := range a.Permission.Rules {
			if !ValidCapability(cap) {
				errs = append(errs, ValidationError{"permission." + cap, fmt.Sprintf("%q is not a capability OpenCode recognizes.", cap)})
				continue
			}
			if !ValidAction(act) {
				errs = append(errs, ValidationError{"permission." + cap, fmt.Sprintf("%q is not a valid permission for %s. Use allow, ask, or deny.", string(act), cap)})
			}
		}
	}

	return errs
}

// RequiresLine extracts an author-declared dependency line from a description,
// per the convention: a line beginning with "Requires:" (case-insensitive).
// It returns the text after the prefix, trimmed, or "" when absent. The import
// preview surfaces this so an adopter sees what the agent expects to be present.
func RequiresLine(description string) string {
	for _, line := range strings.Split(description, "\n") {
		line = strings.TrimSpace(line)
		if len(line) >= 9 && strings.EqualFold(line[:9], "requires:") {
			return strings.TrimSpace(line[9:])
		}
	}
	return ""
}
