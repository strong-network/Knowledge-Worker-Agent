// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package entra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Target is what a connector's metadata says about signing in to it.
type Target struct {
	ServerURL string
	// Scopes are the permissions to ask for, offline_access last.
	Scopes []string
}

var httpClient = &http.Client{
	Timeout: 15 * time.Second,
	// Metadata and tokens come from the address asked; a redirect is refused.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// resourceMetadata is a protected resource's metadata (RFC 9728).
type resourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// Detect reports whether the connector at serverURL signs in with Microsoft
// Entra ID: its protected-resource metadata names Entra as its first
// authorization server. The metadata is read from the well-known address for
// the connector's path, or else for its host.
func Detect(ctx context.Context, serverURL string) (Target, bool, error) {
	srv, err := url.Parse(serverURL)
	if err != nil || !allowedURL(srv) {
		return Target{}, false, fmt.Errorf("unsupported connector address %q", serverURL)
	}
	var md *resourceMetadata
	var lastErr error
	for _, u := range metadataURLs(srv) {
		md, lastErr = fetchMetadata(ctx, u)
		if lastErr == nil {
			break
		}
	}
	if md == nil {
		return Target{}, false, lastErr
	}
	if !covers(md.Resource, srv) {
		return Target{}, false, fmt.Errorf("the connector's metadata describes %q, not %q", md.Resource, serverURL)
	}
	if len(md.AuthorizationServers) == 0 || !isEntra(md.AuthorizationServers[0]) {
		return Target{}, false, nil
	}
	scopes := withOfflineAccess(md.ScopesSupported)
	if len(scopes) < 2 {
		return Target{}, true, errors.New("the connector doesn't say which permission to ask Microsoft for")
	}
	return Target{ServerURL: serverURL, Scopes: scopes}, true, nil
}

// WithScopes returns the target asking for scopes instead of the published ones.
func (t Target) WithScopes(scopes []string) Target {
	t.Scopes = withOfflineAccess(scopes)
	return t
}

// withOfflineAccess returns scopes without blanks or repeats, offline_access last.
func withOfflineAccess(in []string) []string {
	var scopes []string
	seen := map[string]bool{"offline_access": true}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !seen[strings.ToLower(s)] {
			seen[strings.ToLower(s)] = true
			scopes = append(scopes, s)
		}
	}
	return append(scopes, "offline_access")
}

func metadataURLs(srv *url.URL) []string {
	base := url.URL{Scheme: srv.Scheme, Host: srv.Host}
	path := strings.TrimRight(srv.Path, "/")
	var out []string
	if path != "" {
		u := base
		u.Path = "/.well-known/oauth-protected-resource" + path
		out = append(out, u.String())
	}
	u := base
	u.Path = "/.well-known/oauth-protected-resource"
	return append(out, u.String())
}

func fetchMetadata(ctx context.Context, u string) (*resourceMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	var md resourceMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&md); err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	return &md, nil
}

// covers reports whether the resource the metadata describes includes the
// connector's address: the same origin, and a path that starts the connector's.
// A token is sent only to the resource it was asked for.
func covers(resource string, srv *url.URL) bool {
	r, err := url.Parse(resource)
	if err != nil || !strings.EqualFold(r.Scheme, srv.Scheme) || !strings.EqualFold(r.Host, srv.Host) {
		return false
	}
	rp, sp := strings.TrimRight(r.Path, "/"), strings.TrimRight(srv.Path, "/")
	return rp == "" || sp == rp || strings.HasPrefix(sp, rp+"/")
}

// isEntra reports whether an authorization server is Microsoft Entra ID, or
// the fake KWA_ENTRA_AUTHORITY names.
func isEntra(server string) bool {
	u, err := url.Parse(server)
	if err != nil {
		return false
	}
	a := authority()
	return strings.EqualFold(u.Scheme, a.Scheme) && strings.EqualFold(u.Host, a.Host)
}
