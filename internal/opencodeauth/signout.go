// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"context"
	"strings"
)

// SignOut discards the stored GitHub Copilot credentials so another provider
// can be used instead. opencode owns the credential store, so the sign-out goes
// through it rather than by deleting files behind its back.
func SignOut() error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	// opencode's API, not `auth logout`, which asks which account to sign out of.
	return removeCredentials(ctx, Provider)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
