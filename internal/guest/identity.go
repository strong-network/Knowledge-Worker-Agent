// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package guest

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
)

const (
	cookieName   = "kwa_guest"
	oldCookie    = "sds_guest" // the name before the rename; still read
	maxNameLen   = 60
	cookieMaxAge = 180 * 24 * time.Hour
)

// identity is who a guest says they are: attribution, not
// authentication. The Workspace App decides who reaches the port at all; this
// only labels what they write. The id is random and self-issued.
type identity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// cleanName makes a self-declared name safe to store, render and quote:
// control and format characters (including bidi overrides) removed, runs of
// whitespace collapsed, length capped. "" means unusable.
func cleanName(s string) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if n >= maxNameLen {
			break
		}
		if space {
			b.WriteByte(' ')
			n++
			space = false
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// readIdentity returns the guest's identity from the cookie, if any. The value
// is re-cleaned on every read: the cookie is under the guest's control.
func readIdentity(r *http.Request) (identity, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		if c, err = r.Cookie(oldCookie); err != nil {
			return identity{}, false
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return identity{}, false
	}
	var id identity
	if json.Unmarshal(raw, &id) != nil {
		return identity{}, false
	}
	id.Name = cleanName(id.Name)
	if id.ID == "" || len(id.ID) > 64 || id.Name == "" {
		return identity{}, false
	}
	return id, true
}

// writeIdentity sets the cookie. Host-only (no Domain), because cookies are
// not scoped by port and every Workspace App shares the proxy's parent domain.
func writeIdentity(w http.ResponseWriter, id identity) {
	raw, _ := json.Marshal(id)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    base64.RawURLEncoding.EncodeToString(raw),
		Path:     "/",
		MaxAge:   int(cookieMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// newIdentity keeps an existing guest's id when they change their name, so a
// rename does not turn one person into two.
func newIdentity(r *http.Request, name string) identity {
	if prev, ok := readIdentity(r); ok {
		return identity{ID: prev.ID, Name: name}
	}
	return identity{ID: "g-" + strings.ReplaceAll(chat.NewUUID(), "-", ""), Name: name}
}
