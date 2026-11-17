// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"context"
	"fmt"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodev2"
)

// This file holds the generic half of opencode's credential store: an API-key
// credential for an arbitrary provider id. opencode keeps credentials in its
// database, behind its server's API, and accepts one for a provider it has no
// built-in knowledge of, which is what makes a config-declared provider
// authenticatable at all.

// The credential calls, a var each so tests needn't run opencode.
var (
	setCredential     = opencodev2.SetCredential
	removeCredentials = opencodev2.RemoveCredentials
	hasCredential     = opencodev2.HasCredential
)

// SetAPIKey stores an API-key credential for a provider id, replacing the
// credentials it had.
func SetAPIKey(provider, key string) error {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return fmt.Errorf("set api key: empty provider id")
	}
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("set api key: empty key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	return setCredential(ctx, provider, "", map[string]any{"type": "key", "key": key})
}

// RemoveAPIKey deletes a provider's stored credentials. Removing none is not
// an error: the caller's intent (no credential for this provider) is met.
func RemoveAPIKey(provider string) error {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return fmt.Errorf("remove api key: empty provider id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	return removeCredentials(ctx, provider)
}

// HasCredential reports whether opencode holds a credential for a provider id,
// and false when its server can't say. Used by the provider registry to decide
// whether a user-key provider is authenticated.
func HasCredential(provider string) bool {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	has, err := hasCredential(ctx, provider)
	return err == nil && has
}
