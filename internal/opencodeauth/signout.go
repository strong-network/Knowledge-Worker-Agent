// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// SignOut discards the stored GitHub Copilot credentials so another provider
// can be used instead. opencode owns the credential store, so the sign-out goes
// through it rather than by deleting files behind its back.
func SignOut() error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, resolveBin(), "auth", "logout", Provider).CombinedOutput()
	if err == nil {
		return nil
	}
	// Already signed out is the state the caller wanted; not an error.
	if strings.Contains(strings.ToLower(string(out)), "not found") {
		return nil
	}
	if msg := strings.TrimSpace(ansiRe.ReplaceAllString(string(out), "")); msg != "" {
		return fmt.Errorf("%s", firstLine(msg))
	}
	return err
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
