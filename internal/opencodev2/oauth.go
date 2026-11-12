// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodev2

import (
	"context"
	"fmt"
	"net/url"
)

// OAuthMethod is the public, non-secret description of one OpenCode OAuth
// method. It deliberately contains no credential fields.
type OAuthMethod struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// IntegrationConnection is the public status OpenCode exposes for a saved
// integration connection. It contains no credential value.
type IntegrationConnection struct {
	Type   string `json:"type"`
	Method string `json:"method"`
	Status *struct {
		Status string `json:"status"`
	} `json:"status,omitempty"`
}

// OAuthAttempt contains only the browser handoff data returned by OpenCode.
// Tokens are created and stored inside OpenCode and are never read here.
type OAuthAttempt struct {
	AttemptID    string `json:"attemptID"`
	URL          string `json:"url"`
	Instructions string `json:"instructions"`
	Mode         string `json:"mode"`
}

type OAuthAttemptStatus struct {
	Status string `json:"status"`
}

type integrationInfo struct {
	Methods     []OAuthMethod           `json:"methods"`
	Connections []IntegrationConnection `json:"connections"`
}

func integrationPath(integrationID, directory string) string {
	query := url.Values{}
	query.Set("directory", directory)
	return "/api/integration/" + url.PathEscape(integrationID) + "?" + query.Encode()
}

func oauthPath(integrationID, attemptID, directory string) string {
	query := url.Values{}
	query.Set("directory", directory)
	path := "/api/integration/" + url.PathEscape(integrationID) + "/connect/oauth"
	if attemptID != "" {
		path += "/" + url.PathEscape(attemptID)
	}
	return path + "?" + query.Encode()
}

// OAuthIntegration reads the provider's OAuth method list and connection
// status. OpenCode's integration API omits credential values from this
// response, unlike its credential-list API.
func OAuthIntegration(ctx context.Context, integrationID, directory string) ([]OAuthMethod, []IntegrationConnection, error) {
	var resp struct {
		Data integrationInfo `json:"data"`
	}
	if err := Get(ctx, integrationPath(integrationID, directory), &resp); err != nil {
		return nil, nil, err
	}
	return resp.Data.Methods, resp.Data.Connections, nil
}

// HasOAuthConnection reports whether OpenCode has an OAuth connection that
// does not currently report an authentication failure. It never retrieves the
// underlying credential value.
func HasOAuthConnection(ctx context.Context, integrationID, directory string) (bool, error) {
	_, connections, err := OAuthIntegration(ctx, integrationID, directory)
	if err != nil {
		return false, err
	}
	for _, connection := range connections {
		if connection.Type != "credential" || connection.Method != "oauth" {
			continue
		}
		if connection.Status != nil && connection.Status.Status == "needs_auth" {
			continue
		}
		return true, nil
	}
	return false, nil
}

// StartOAuth asks OpenCode to begin one of the integration's native OAuth
// flows. OpenCode owns PKCE, polling, callback handling, token exchange, and
// credential storage.
func StartOAuth(ctx context.Context, integrationID, methodID, directory string) (OAuthAttempt, error) {
	var resp struct {
		Data OAuthAttempt `json:"data"`
	}
	path := oauthPath(integrationID, "", directory)
	err := Post(ctx, path, map[string]string{"methodID": methodID}, &resp)
	if err != nil {
		return OAuthAttempt{}, fmt.Errorf("start OpenCode OAuth flow: %w", err)
	}
	return resp.Data, nil
}

func GetOAuthStatus(ctx context.Context, integrationID, attemptID, directory string) (OAuthAttemptStatus, error) {
	var resp struct {
		Data OAuthAttemptStatus `json:"data"`
	}
	if err := Get(ctx, oauthPath(integrationID, attemptID, directory), &resp); err != nil {
		return OAuthAttemptStatus{}, err
	}
	return resp.Data, nil
}

func CancelOAuth(ctx context.Context, integrationID, attemptID, directory string) error {
	return Delete(ctx, oauthPath(integrationID, attemptID, directory))
}
