// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package entra

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodev2"
)

// credentialLabel names the credential in opencode's store.
const credentialLabel = "Microsoft sign-in by Knowledge Worker Agent"

// Stand-ins for opencode 2.x's API, which tests replace.
var (
	integrationOf = opencodev2.ConnectorIntegration
	setCredential = opencodev2.SetCredential
	signedIn      = opencodev2.ConnectorSignedIn
)

// Pending is a sign-in waiting for the address the browser lands on.
type Pending struct {
	server   string
	app      App
	target   Target
	state    string
	verifier string
	// URL is the Microsoft sign-in page to open.
	URL string
}

// Begin starts a sign-in to the connector called server.
func Begin(server string, app App, target Target) *Pending {
	p := &Pending{server: server, app: app, target: target, state: randomString(), verifier: randomString()}
	p.URL = authorizeURL(app, target.Scopes, p.state, p.verifier)
	return p
}

// AddressError is a pasted address that isn't the one this sign-in waits for;
// the sign-in goes on.
type AddressError struct{ msg string }

func (e AddressError) Error() string { return e.msg }

// Complete redeems the code in the address the user pasted, keeps the sign-in,
// and hands the access token to opencode. The same sign-in page can be opened
// again after a refusal, so the sign-in stays usable. When only the handoff
// fails, the sign-in is kept and returned with the error.
func (p *Pending) Complete(ctx context.Context, pasted string) (*Record, error) {
	code, err := p.codeIn(pasted)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	t, err := exchange(ctx, p.app, code, p.verifier, p.target.Scopes)
	if err != nil {
		return nil, err
	}
	if t.RefreshToken == "" {
		return nil, errors.New("Microsoft sign-in gave no refresh token: the app registration needs the offline_access permission")
	}
	r := &Record{
		Server:       strings.ToLower(strings.TrimSpace(p.server)),
		ServerURL:    p.target.ServerURL,
		ClientID:     p.app.ClientID,
		Tenant:       p.app.Tenant,
		Scopes:       p.target.Scopes,
		Account:      accountOf(t.AccessToken),
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		ExpiresAt:    t.expiresAt(now),
	}
	storeMu.Lock()
	err = saveLocked(r)
	storeMu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := hand(ctx, r); err != nil {
		return r, fmt.Errorf("signed in to Microsoft, but opencode didn't take the token: %w", err)
	}
	log.Printf("[entra] %s: signed in as %s", r.Server, r.Account)
	return r, nil
}

// codeIn checks that pasted is where this sign-in sends the browser back to,
// and returns its code, or Microsoft's refusal.
func (p *Pending) codeIn(pasted string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(pasted))
	if err != nil || u.Scheme != "http" || !strings.EqualFold(u.Hostname(), "localhost") || u.Port() != "" ||
		(u.Path != "" && u.Path != "/") {
		return "", AddressError{"That isn't the address this sign-in sends you back to. It starts with http://localhost/?code=."}
	}
	q := u.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(p.state)) != 1 {
		return "", AddressError{"That address belongs to another sign-in. Open the sign-in page again from here, and paste the address you land on."}
	}
	if e := q.Get("error"); e != "" {
		return "", newError(e, q.Get("error_description"), nil, 0)
	}
	code := q.Get("code")
	if code == "" {
		return "", AddressError{"That address is missing the authorization code. Copy the whole address, including everything after the question mark."}
	}
	return code, nil
}

// hand stores r's access token as the connector's credential in opencode 2.x,
// which reconnects the connector with it.
func hand(ctx context.Context, r *Record) error {
	integration, err := integrationOf(ctx, r.Server)
	if err != nil {
		return err
	}
	return setCredential(ctx, integration, credentialLabel, map[string]any{
		"type":     "oauth",
		"methodID": integration,
		"access":   r.AccessToken,
		"refresh":  "",
		"expires":  r.ExpiresAt.UnixMilli(),
		"metadata": map[string]any{
			"serverUrl": r.ServerURL,
			"tokenType": "Bearer",
			"scope":     strings.Join(r.Scopes, " "),
			"client":    map[string]any{"client_id": r.ClientID},
		},
	})
}
