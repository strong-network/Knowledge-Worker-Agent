// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package workspaceapps talks to the workspace sidecar's Workspace App routes.
// Unlike the heartbeat, which the sidecar signs itself, these routes
// forward the caller's workspace token, so every call carries the owner's token
// taken from the owner's own request. The token is never stored or logged.
package workspaceapps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
)

// socketPath is a var so tests can point the client at a fake sidecar.
var socketPath = "/var/strong-network/socks/sidecar-ipc"

func init() {
	// Local development only: a scratch server talks to a fake sidecar.
	if p := env.Get("KWA_SIDECAR_SOCKET"); p != "" {
		socketPath = p
	}
}

// ownerTokenHeader is injected by the platform proxy on custom-access-item
// traffic, which is how Knowledge Worker Agent is always opened.
const ownerTokenHeader = "cloud_editor_user_token"

// OwnerToken returns the owner's workspace token from an owner request, or "".
func OwnerToken(r *http.Request) string { return r.Header.Get(ownerTokenHeader) }

// App is one Workspace App as the sidecar reports it.
type App struct {
	ID                uint64   `json:"id"`
	Port              uint32   `json:"port"`
	Name              string   `json:"name"`
	URL               string   `json:"url"`
	Online            bool     `json:"online"`
	UserIDs           []uint64 `json:"user_ids"`
	IsPublic          bool     `json:"is_public"`
	IsSharedWithTeam  bool     `json:"is_shared_with_team"`
	HTTPSEnabled      bool     `json:"is_https_enabled"`
	OverrideHost      bool     `json:"override_host"`
	OverrideHostValue string   `json:"override_host_value"`
}

// Member is a project member the app can be shared with.
type Member struct {
	ID    uint64 `json:"id"`
	Email string `json:"email"`
}

// Error is a non-2xx sidecar reply. Message is the gRPC status text, which is
// shown to the owner but never logged.
type Error struct {
	Status  int
	Code    int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("sidecar returned HTTP %d (grpc code %d)", e.Status, e.Code)
}

var client = &http.Client{
	Transport: &http.Transport{
		// localhost is only the HTTP Host; the socket is the destination.
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
		MaxIdleConns:    1,
		IdleConnTimeout: 30 * time.Second,
	},
	Timeout: 10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// List returns the workspace's apps (GET /internal/v1/workspace_app:list).
func List(ctx context.Context, token string) ([]App, error) {
	var out struct {
		WorkspaceApps []wireApp `json:"workspaceApps"`
	}
	if err := call(ctx, http.MethodGet, "/internal/v1/workspace_app:list", token, nil, &out); err != nil {
		return nil, err
	}
	apps := make([]App, 0, len(out.WorkspaceApps))
	for _, w := range out.WorkspaceApps {
		apps = append(apps, w.app())
	}
	return apps, nil
}

// Create makes a private, plain-HTTP app on port, shared with the given project
// members (and the whole project when projectSharing). The reply omits the
// audience; List is the only route that returns it.
func Create(ctx context.Context, token string, port uint32, name string, projectSharing bool, userIDs []uint64) (App, error) {
	var out wireApp
	err := call(ctx, http.MethodPost, "/internal/v1/workspace_app:create", token, map[string]any{
		"port":           strconv.FormatUint(uint64(port), 10),
		"appName":        name,
		"isPublic":       false,
		"projectSharing": projectSharing,
		"sharedWith":     idStrings(userIDs),
		"isHttpsEnabled": false,
		"overrideHost":   false,
	}, &out)
	return out.app(), err
}

// Edit replaces every field of the app with a's: the platform
// assigns them all from the request, so a must be a listed app with only the
// intended change made.
func Edit(ctx context.Context, token string, a App) error {
	return call(ctx, http.MethodPost, "/internal/v1/workspace_app:edit", token, map[string]any{
		"wsAppId":           strconv.FormatUint(a.ID, 10),
		"appName":           a.Name,
		"isPublic":          a.IsPublic,
		"projectSharing":    a.IsSharedWithTeam,
		"sharedWith":        idStrings(a.UserIDs),
		"isHttpsEnabled":    a.HTTPSEnabled,
		"overrideHost":      a.OverrideHost,
		"overrideHostValue": a.OverrideHostValue,
	}, nil)
}

// Delete removes the app.
func Delete(ctx context.Context, token string, id uint64) error {
	return call(ctx, http.MethodPost, "/internal/v1/workspace_app:delete", token,
		map[string]any{"appId": strconv.FormatUint(id, 10)}, nil)
}

// Members lists the workspace's project members, the owner included.
func Members(ctx context.Context, token string) ([]Member, error) {
	var out struct {
		Users []struct {
			UserID uint64Str `json:"userId"`
			Email  string    `json:"email"`
		} `json:"users"`
	}
	if err := call(ctx, http.MethodGet, "/internal/v1/project/users", token, nil, &out); err != nil {
		return nil, err
	}
	members := make([]Member, 0, len(out.Users))
	for _, u := range out.Users {
		members = append(members, Member{ID: uint64(u.UserID), Email: u.Email})
	}
	return members, nil
}

// idStrings writes uint64 ids as proto3 JSON does, as decimal strings, so no id
// is rounded through a float on the way.
func idStrings(ids []uint64) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.FormatUint(id, 10))
	}
	return out
}

func call(ctx context.Context, method, path, token string, body, into any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("workspacetoken", token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		e := &Error{Status: resp.StatusCode}
		var st struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &st) == nil {
			e.Code, e.Message = st.Code, st.Message
		}
		return e
	}
	if into == nil {
		return nil
	}
	return json.Unmarshal(raw, into)
}

// wireApp is the grpc-gateway JSON shape: lowerCamelCase names, and uint64
// fields as decimal strings (proto3 JSON), which uint64Str also accepts bare.
type wireApp struct {
	ID                uint64Str   `json:"id"`
	Port              uint32      `json:"port"`
	Name              string      `json:"name"`
	URL               string      `json:"url"`
	Online            bool        `json:"online"`
	UserIDs           []uint64Str `json:"userIds"`
	IsPublic          bool        `json:"isPublic"`
	IsSharedWithTeam  bool        `json:"isSharedWithTeam"`
	IsHTTPSEnabled    bool        `json:"isHttpsEnabled"`
	OverrideHost      bool        `json:"overrideHost"`
	OverrideHostValue string      `json:"overrideHostValue"`
}

func (w wireApp) app() App {
	ids := make([]uint64, 0, len(w.UserIDs))
	for _, id := range w.UserIDs {
		ids = append(ids, uint64(id))
	}
	return App{
		ID: uint64(w.ID), Port: w.Port, Name: w.Name, URL: w.URL, Online: w.Online,
		UserIDs: ids, IsPublic: w.IsPublic, IsSharedWithTeam: w.IsSharedWithTeam,
		HTTPSEnabled: w.IsHTTPSEnabled, OverrideHost: w.OverrideHost, OverrideHostValue: w.OverrideHostValue,
	}
}

type uint64Str uint64

func (u *uint64Str) UnmarshalJSON(b []byte) error {
	s := string(bytes.Trim(b, `"`))
	if s == "" || s == "null" {
		*u = 0
		return nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return err
	}
	*u = uint64Str(v)
	return nil
}
