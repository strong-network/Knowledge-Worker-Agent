// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodev2

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Credentials.
//
// opencode keeps credentials in its database, so a sign-in or a key that KWA
// stores goes to the server's API.
//
// A credential belongs to an integration: a model provider's id
// ("github-copilot", or a provider the configuration declares), or a
// connector's own id, which the server reports with the connector.

// credential is a stored credential, without its secret: of its value only the
// type ("key" or "oauth") is read.
type credential struct {
	ID            string `json:"id"`
	IntegrationID string `json:"integrationID"`
	Value         struct {
		Type string `json:"type"`
	} `json:"value"`
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

// RemoveCredentialsOfType removes integration's credentials of one type, "key"
// or "oauth", and keeps the others.
func RemoveCredentialsOfType(ctx context.Context, integration, valueType string) error {
	all, err := credentials(ctx)
	if err != nil {
		return err
	}
	for _, c := range all {
		if strings.EqualFold(c.IntegrationID, integration) && c.Value.Type == valueType {
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
// mcp list` did on 1.x; one switched off, or still connecting after that,
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

// ConnectorIntegration returns the integration a credential for the connector
// called name belongs to.
func ConnectorIntegration(ctx context.Context, name string) (string, error) {
	c, err := findConnector(ctx, name)
	if err != nil {
		return "", err
	}
	if c.IntegrationID == "" {
		return "", fmt.Errorf("%s keeps no sign-in", name)
	}
	return c.IntegrationID, nil
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
