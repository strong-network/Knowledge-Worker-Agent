// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package entra

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func randomString() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func challengeOf(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func authorizeURL(app App, scopes []string, state, verifier string) string {
	q := url.Values{
		"client_id":             {app.ClientID},
		"response_type":         {"code"},
		"response_mode":         {"query"},
		"redirect_uri":          {RedirectURI},
		"scope":                 {strings.Join(scopes, " ")},
		"state":                 {state},
		"code_challenge":        {challengeOf(verifier)},
		"code_challenge_method": {"S256"},
		"prompt":                {"select_account"},
	}
	return endpoint(app, "authorize") + "?" + q.Encode()
}

// tokens is the token endpoint's answer.
type tokens struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	ExpiresIn    seconds `json:"expires_in"`
}

// seconds accepts a number, or a number in a string.
type seconds int64

func (s *seconds) UnmarshalJSON(b []byte) error {
	n, err := strconv.ParseInt(strings.Trim(string(b), `"`), 10, 64)
	*s = seconds(n)
	return err
}

func exchange(ctx context.Context, app App, code, verifier string, scopes []string) (tokens, error) {
	return redeem(ctx, app, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {RedirectURI},
		"code_verifier": {verifier},
		"scope":         {strings.Join(scopes, " ")},
	})
}

func refresh(ctx context.Context, app App, refreshToken string, scopes []string) (tokens, error) {
	return redeem(ctx, app, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"scope":         {strings.Join(scopes, " ")},
	})
}

func redeem(ctx context.Context, app App, form url.Values) (tokens, error) {
	form.Set("client_id", app.ClientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(app, "token"), strings.NewReader(form.Encode()))
	if err != nil {
		return tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return tokens{}, fmt.Errorf("could not reach Microsoft sign-in: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokens{}, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
			Codes       []int  `json:"error_codes"`
		}
		_ = json.Unmarshal(body, &e)
		return tokens{}, newError(e.Error, e.Description, e.Codes, resp.StatusCode)
	}
	var t tokens
	if err := json.Unmarshal(body, &t); err != nil || t.AccessToken == "" {
		return tokens{}, fmt.Errorf("Microsoft sign-in answered without a token")
	}
	return t, nil
}

func (t tokens) expiresAt(now time.Time) time.Time {
	if t.ExpiresIn <= 0 {
		return now.Add(time.Hour)
	}
	return now.Add(time.Duration(t.ExpiresIn) * time.Second)
}

// Error is a refusal from Microsoft sign-in, in words a person can act on.
type Error struct {
	Code        string // OAuth error, such as invalid_grant
	AADSTS      int    // Entra's own code, 0 when none was given
	Description string
	// Expired is true when only signing in again helps.
	Expired bool
}

func newError(code, description string, codes []int, status int) *Error {
	e := &Error{Code: code, Description: strings.TrimSpace(description)}
	if len(codes) > 0 {
		e.AADSTS = codes[0]
	} else if n, ok := aadsts(description); ok {
		e.AADSTS = n
	}
	if e.Code == "" {
		e.Code = fmt.Sprintf("HTTP %d", status)
	}
	e.Expired = code == "invalid_grant"
	return e
}

// aadsts finds the code in a description that starts "AADSTS65001: ...".
func aadsts(description string) (int, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(description), "AADSTS")
	if !ok {
		return 0, false
	}
	end := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		end = len(rest)
	}
	n, err := strconv.Atoi(rest[:end])
	return n, err == nil
}

var messages = map[int]string{
	50011:   "Microsoft refused the return address. The app registration needs the redirect URI http://localhost, under Mobile and desktop applications.",
	50105:   "Your account isn't assigned to this app in Microsoft Entra ID. Ask your administrator to add you.",
	53003:   "A sign-in policy of your organization blocked this sign-in.",
	54005:   "That sign-in code was already used. Sign in again.",
	65001:   "An administrator has to approve this app for your organization first.",
	70008:   "The sign-in has expired. Sign in again.",
	70043:   "Your Microsoft sign-in has expired. Sign in again.",
	700016:  "Microsoft can't find the app in your organization. Check KWA_ENTRA_CLIENT_ID and KWA_ENTRA_TENANT_ID.",
	700082:  "Your Microsoft sign-in has expired. Sign in again.",
	7000218: "The app registration must allow public clients without a secret: register the redirect URI under Mobile and desktop applications.",
}

func (e *Error) Error() string {
	if m, ok := messages[e.AADSTS]; ok {
		return fmt.Sprintf("%s (AADSTS%d)", m, e.AADSTS)
	}
	if e.Code == "access_denied" {
		return "The sign-in was cancelled."
	}
	d := e.Description
	if i := strings.IndexAny(d, "\r\n"); i >= 0 {
		d = d[:i]
	}
	if d == "" {
		return "Microsoft sign-in refused: " + e.Code
	}
	return "Microsoft sign-in refused: " + d
}

// accountOf returns the signed-in account the access token names, for display
// only: the token isn't verified, and nothing else reads it.
func accountOf(accessToken string) string {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if json.Unmarshal(raw, &claims) != nil {
		return ""
	}
	for _, k := range []string{"preferred_username", "upn", "unique_name", "email"} {
		if s, ok := claims[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}
