// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package entra signs in to connectors protected by Microsoft Entra ID, which
// opencode can't do: 2.x rejects the issuer Entra publishes for its multi-tenant
// addresses, and Entra has no dynamic client registration. Knowledge Worker
// Agent signs in itself, at the tenant's own address with an app registered
// there, keeps the refresh token, and hands opencode only the access token.
package entra

import (
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
)

// RedirectURI is where Entra sends the browser back to. Nothing listens there:
// the user pastes the address they land on, as for every connector.
const RedirectURI = "http://localhost"

const defaultAuthority = "https://login.microsoftonline.com"

// App is the app registration Knowledge Worker Agent signs in with.
type App struct {
	ClientID string
	Tenant   string
}

var (
	clientIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	tenantRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)
)

// Configured returns the app KWA_ENTRA_CLIENT_ID and KWA_ENTRA_TENANT_ID name,
// and whether both are set and well formed.
func Configured() (App, bool) {
	return NewApp(env.Get("KWA_ENTRA_CLIENT_ID"), env.Get("KWA_ENTRA_TENANT_ID"))
}

// NewApp returns the app with this client ID and tenant, and whether both are
// well formed.
func NewApp(clientID, tenant string) (App, bool) {
	app := App{ClientID: strings.TrimSpace(clientID), Tenant: strings.TrimSpace(tenant)}
	return app, clientIDRe.MatchString(app.ClientID) && tenantRe.MatchString(app.Tenant)
}

// authority is Microsoft's sign-in service, or the fake that
// KWA_ENTRA_AUTHORITY names for local development.
func authority() *url.URL {
	if v := strings.TrimSpace(env.Get("KWA_ENTRA_AUTHORITY")); v != "" {
		if u, err := url.Parse(strings.TrimRight(v, "/")); err == nil && allowedURL(u) {
			return u
		}
	}
	u, _ := url.Parse(defaultAuthority)
	return u
}

// allowedURL accepts https, and http only to this machine.
func allowedURL(u *url.URL) bool {
	switch u.Scheme {
	case "https":
		return u.Host != ""
	case "http":
		return isLoopback(u.Hostname())
	}
	return false
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// endpoint is one of the tenant's OAuth addresses, "authorize" or "token".
func endpoint(app App, name string) string {
	u := *authority()
	u.Path = strings.TrimRight(u.Path, "/") + "/" + url.PathEscape(app.Tenant) + "/oauth2/v2.0/" + name
	return u.String()
}
