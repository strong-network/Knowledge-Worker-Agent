// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodev2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// Credentials on 2.x.
//
// 2.x keeps credentials in its database, and reads opencode's auth.json only
// once, when it first upgrades a 1.x database. So on 2.x a sign-in or a key
// that KWA stores goes to the server's API. KWA writes auth.json as well,
// which keeps it for 1.x if the installation goes back.
//
// A credential belongs to an integration: a model provider's id
// ("github-copilot", or a provider the configuration declares), or a
// connector's own id, which the server reports with the connector.

// credential is a stored credential, without its secret.
type credential struct {
	ID            string `json:"id"`
	IntegrationID string `json:"integrationID"`
}

func credentials(ctx context.Context) ([]credential, error) {
	var resp struct {
		Data []credential `json:"data"`
	}
	if err := Get(ctx, "/api/credential", &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// HasCredential reports whether 2.x holds a credential for integration.
func HasCredential(ctx context.Context, integration string) (bool, error) {
	all, err := credentials(ctx)
	if err != nil {
		return false, err
	}
	for _, c := range all {
		if strings.EqualFold(c.IntegrationID, integration) {
			return true, nil
		}
	}
	return false, nil
}

// SetCredential stores value as integration's credential and removes the ones
// it had, so a new sign-in replaces the old one rather than adding an account.
// The old ones go only once the new one is stored.
func SetCredential(ctx context.Context, integration, label string, value map[string]any) error {
	old, err := credentials(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{"integrationID": integration, "value": value, "activate": true}
	if label != "" {
		body["label"] = label
	}
	var created struct {
		Data credential `json:"data"`
	}
	if err := Post(ctx, "/api/credential", body, &created); err != nil {
		return withoutReply(err)
	}
	for _, c := range old {
		if c.IntegrationID == integration && c.ID != created.Data.ID {
			if err := Delete(ctx, "/api/credential/"+url.PathEscape(c.ID)); err != nil {
				return err
			}
		}
	}
	return nil
}

// RemoveCredentials removes every credential 2.x holds for integration.
func RemoveCredentials(ctx context.Context, integration string) error {
	all, err := credentials(ctx)
	if err != nil {
		return err
	}
	for _, c := range all {
		if strings.EqualFold(c.IntegrationID, integration) {
			if err := Delete(ctx, "/api/credential/"+url.PathEscape(c.ID)); err != nil {
				return err
			}
		}
	}
	return nil
}

// connector is a connector as the server lists it.
type connector struct {
	Name          string `json:"name"`
	IntegrationID string `json:"integrationID"`
	Status        struct {
		Status string `json:"status"`
	} `json:"status"`
}

// listConnectors lists the connectors once the server has set them up. Right
// after a start or a reload it answers an empty list, then connectors still
// connecting, so it asks again for a while (folderWarmTimeout), unless the
// server has no connectors at all, and then returns what it last heard.
func listConnectors(ctx context.Context) ([]connector, error) {
	ctx, cancel := context.WithTimeout(ctx, folderWarmTimeout)
	defer cancel()
	var last []connector
	for {
		var resp struct {
			Data []connector `json:"data"`
		}
		if err := Get(ctx, "/api/mcp", &resp); err != nil {
			if last != nil && ctx.Err() != nil {
				return last, nil
			}
			return nil, err
		}
		last = resp.Data
		settled := len(last) > 0 || connectorCount.Load() == 0
		for _, c := range last {
			settled = settled && c.Status.Status != "pending"
		}
		if settled {
			return last, nil
		}
		select {
		case <-ctx.Done():
			return last, nil
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// ConnectorStatuses returns whether each connector is connected, by name in
// lower case. It waits a little for connectors still connecting, as `opencode
// mcp list` does on 1.x; one switched off, or still connecting after that,
// gives no verdict and is left out.
func ConnectorStatuses(ctx context.Context) (map[string]bool, error) {
	all, err := listConnectors(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, c := range all {
		switch c.Status.Status {
		case "pending", "disabled":
		default:
			out[strings.ToLower(c.Name)] = c.Status.Status == "connected"
		}
	}
	return out, nil
}

// SignOutConnector removes the credentials of the connector called name.
func SignOutConnector(ctx context.Context, name string) error {
	c, err := findConnector(ctx, name)
	if err != nil {
		return err
	}
	if c.IntegrationID == "" {
		return fmt.Errorf("%s keeps no sign-in", name)
	}
	return RemoveCredentials(ctx, c.IntegrationID)
}

// ConnectorSignedIn reports whether 2.x holds a credential for the connector
// called name.
func ConnectorSignedIn(ctx context.Context, name string) (bool, error) {
	c, err := findConnector(ctx, name)
	if err != nil || c.IntegrationID == "" {
		return false, err
	}
	return HasCredential(ctx, c.IntegrationID)
}

func findConnector(ctx context.Context, name string) (connector, error) {
	all, err := listConnectors(ctx)
	if err != nil {
		return connector{}, err
	}
	for _, c := range all {
		if strings.EqualFold(c.Name, name) {
			return c, nil
		}
	}
	return connector{}, fmt.Errorf("no connector called %s", name)
}

// importMarker records that the connector sign-ins of 1.x were imported.
func importMarker() string { return filepath.Join(layout.System(), "opencode-v2-connectors-imported") }

// v1Connector is a connector sign-in as 1.x keeps it in mcp-auth.json.
type v1Connector struct {
	Tokens *struct {
		AccessToken  string  `json:"accessToken"`
		RefreshToken string  `json:"refreshToken"`
		ExpiresAt    float64 `json:"expiresAt"`
		Scope        string  `json:"scope"`
	} `json:"tokens"`
	ClientInfo *struct {
		ClientID              string `json:"clientId"`
		ClientSecret          string `json:"clientSecret"`
		ClientIDIssuedAt      int64  `json:"clientIdIssuedAt"`
		ClientSecretExpiresAt *int64 `json:"clientSecretExpiresAt"`
	} `json:"clientInfo"`
	ServerURL string `json:"serverUrl"`
}

// ImportConnectorSignIns copies the connector sign-ins 1.x kept in the file
// path (its mcp-auth.json) into 2.x, and returns how many it copied. 2.x
// imports auth.json when it upgrades a 1.x database but not this file, so
// every connector would otherwise need signing in again.
//
// It runs once per installation, so a later sign-out sticks. A sign-in is
// copied only to a connector of the same name and address that 2.x holds no
// credential for: a token goes to no server but the one that issued it.
func ImportConnectorSignIns(ctx context.Context, path string) (int, error) {
	if _, err := os.Stat(importMarker()); err == nil {
		return 0, nil
	}
	n, sawConnectors, err := importConnectorSignIns(ctx, path)
	if err != nil {
		return n, err
	}
	// An empty list may only mean the server wasn't ready; try again next time.
	if !sawConnectors {
		return n, nil
	}
	if err := os.MkdirAll(filepath.Dir(importMarker()), 0o755); err != nil {
		return n, err
	}
	return n, os.WriteFile(importMarker(), []byte(fmt.Sprintf("%d\n", n)), 0o644)
}

func importConnectorSignIns(ctx context.Context, path string) (n int, sawConnectors bool, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	var v1 map[string]v1Connector
	if err := json.Unmarshal(raw, &v1); err != nil {
		return 0, false, fmt.Errorf("read %s: %w", path, err)
	}
	servers, err := listConnectors(ctx)
	if err != nil {
		return 0, false, err
	}
	if len(servers) == 0 {
		return 0, false, nil
	}
	have, err := credentials(ctx)
	if err != nil {
		return 0, true, err
	}
	stored := map[string]bool{}
	for _, c := range have {
		stored[c.IntegrationID] = true
	}
	urls := connectorURLs()
	for _, s := range servers {
		e, ok := v1[s.Name]
		if !ok || s.IntegrationID == "" || stored[s.IntegrationID] || e.Tokens == nil || e.Tokens.AccessToken == "" {
			continue
		}
		if !sameURL(e.ServerURL, urls[s.Name]) {
			continue
		}
		if err := SetCredential(ctx, s.IntegrationID, "Imported from opencode 1.x", v2SignIn(s.IntegrationID, e)); err != nil {
			return n, true, fmt.Errorf("%s: %w", s.Name, err)
		}
		log.Printf("[opencode-v2] imported the 1.x sign-in of connector %s", s.Name)
		n++
	}
	return n, true, nil
}

// v2SignIn is a 1.x sign-in in the shape 2.x stores: the tokens, with the
// client the authorization server registered and the scope in the metadata.
// 1.x gives the expiry in seconds, 2.x in milliseconds, and 0 means none.
func v2SignIn(integration string, e v1Connector) map[string]any {
	meta := map[string]any{"serverUrl": e.ServerURL, "tokenType": "Bearer"}
	if e.Tokens.Scope != "" {
		meta["scope"] = e.Tokens.Scope
	}
	if ci := e.ClientInfo; ci != nil && ci.ClientID != "" {
		client := map[string]any{"client_id": ci.ClientID}
		if ci.ClientSecret != "" {
			client["client_secret"] = ci.ClientSecret
		}
		if ci.ClientIDIssuedAt != 0 {
			client["client_id_issued_at"] = ci.ClientIDIssuedAt
		}
		if ci.ClientSecretExpiresAt != nil {
			client["client_secret_expires_at"] = *ci.ClientSecretExpiresAt
		}
		meta["client"] = client
	}
	return map[string]any{
		"type":     "oauth",
		"methodID": integration,
		"access":   e.Tokens.AccessToken,
		"refresh":  e.Tokens.RefreshToken,
		"expires":  int64(e.Tokens.ExpiresAt * 1000),
		"metadata": meta,
	}
}

// connectorURLs returns each configured connector's address: the global
// configuration's, then the platform's over it, as 2.x layers them.
func connectorURLs() map[string]string {
	out := map[string]string{}
	paths := []string{globalConfigPath()}
	if dir := os.Getenv("OPENCODE_CONFIG_DIR"); dir != "" {
		paths = append(paths, filepath.Join(dir, "opencode.json"))
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var doc struct {
			MCP map[string]struct {
				URL string `json:"url"`
			} `json:"mcp"`
		}
		if json.Unmarshal(b, &doc) != nil {
			continue
		}
		for name, s := range doc.MCP {
			if s.URL != "" {
				out[name] = s.URL
			}
		}
	}
	return out
}

func sameURL(a, b string) bool {
	a, b = strings.TrimRight(strings.TrimSpace(a), "/"), strings.TrimRight(strings.TrimSpace(b), "/")
	return a != "" && a == b
}
