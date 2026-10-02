// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package sharing is the owner's side of chat sharing: starting and stopping sharing
// a chat, the Workspace App that carries guests to it, and who that app is
// shared with. Every platform call borrows the owner's token from the owner's
// own request and nothing runs in the background, so the token is never kept.
// The guest listener must never import this package.
package sharing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/guest"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workspaceapps"
)

// appName is what the platform shows for the app Knowledge Worker Agent creates.
// Apps are found by port, so one created under an earlier name keeps working.
const appName = "Knowledge Worker Agent shared chats"

// The platform and the guest listener, as variables so tests can stand in.
var (
	listApps    = workspaceapps.List
	createApp   = workspaceapps.Create
	editApp     = workspaceapps.Edit
	deleteApp   = workspaceapps.Delete
	listMembers = workspaceapps.Members
	guestPort   = guest.Port
)

// appMu serialises app changes, so two tabs sharing at once cannot both
// create one: the platform allows one app per port.
var appMu sync.Mutex

// Participant is one person who opened the chat, and whether they are here now.
type Participant struct {
	db.Guest
	Present bool `json:"present"`
}

// State is GET /api/sessions/{id}/share.
type State struct {
	Shared       bool          `json:"shared"`
	ShareURL     string        `json:"share_url"`
	Online       bool          `json:"online"`
	Present      int           `json:"present"`
	Participants []Participant `json:"participants"`
	// Problem says why the link cannot be shown, when the chat is shared.
	Problem string `json:"problem,omitempty"`
	db.ShareOptions
	// FilesFolder is the folder guests see with files on; FilesUnavailable
	// says why files cannot be turned on, when they cannot.
	FilesFolder      string `json:"files_folder,omitempty"`
	FilesUnavailable string `json:"files_unavailable,omitempty"`
}

type failure struct {
	status int
	msg    string
}

func (f *failure) Error() string { return f.msg }

var errNoToken = &failure{http.StatusForbidden, "Open Knowledge Worker Agent from your workspace to share chats. This request did not carry your workspace credential."}

// platformFailure turns a sidecar error into what the owner is told. The
// platform's own message is shown, never logged.
func platformFailure(action string, err error) *failure {
	log.Printf("[sharing] %s failed: %v", action, err)
	var se *workspaceapps.Error
	if errors.As(err, &se) && se.Message != "" {
		return &failure{http.StatusBadGateway, "Sharing could not be set up: " + se.Message}
	}
	return &failure{http.StatusBadGateway, "Sharing could not be set up: the workspace platform did not answer."}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeFailure(w http.ResponseWriter, err error) {
	var f *failure
	if !errors.As(err, &f) {
		f = &failure{http.StatusInternalServerError, "Sharing could not be changed."}
		log.Printf("[sharing] %v", err)
	}
	writeJSON(w, f.status, map[string]string{"error": f.msg})
}

// findApp returns the app on the guest port, if there is one. The port is the
// app's identity: there is no id to store and nothing to drift.
func findApp(ctx context.Context, token string) (workspaceapps.App, bool, error) {
	port, _ := guestPort()
	apps, err := listApps(ctx, token)
	if err != nil {
		return workspaceapps.App{}, false, platformFailure("list apps", err)
	}
	for _, a := range apps {
		if int(a.Port) == port {
			return a, true, nil
		}
	}
	return workspaceapps.App{}, false, nil
}

// ensureApp finds the guest app or creates it with the remembered audience.
func ensureApp(ctx context.Context, token string) (workspaceapps.App, error) {
	app, ok, err := findApp(ctx, token)
	if err != nil || ok {
		return app, err
	}
	port, _ := guestPort()
	aud := db.RememberedAudience()
	created, err := createApp(ctx, token, uint32(port), appName, aud.Project, aud.UserIDs)
	if err != nil {
		return workspaceapps.App{}, platformFailure("create app", err)
	}
	log.Printf("[sharing] created the workspace app on port %d", port)
	return created, nil
}

// retireAppIfUnused deletes the guest app once no chat is shared, and forgets
// its audience: the next share starts from nobody, as the first did. A failure
// leaves an app with nothing shared behind it, which exposes nothing; the next
// call tries again.
func retireAppIfUnused(ctx context.Context, token string) {
	if token == "" || len(db.SharedSessionIDs()) > 0 {
		return
	}
	app, ok, err := findApp(ctx, token)
	if err != nil || !ok {
		return
	}
	if err := db.RememberAudience(db.Audience{}); err != nil {
		log.Printf("[sharing] forget audience: %v", err)
	}
	if err := deleteApp(ctx, token, app.ID); err != nil {
		platformFailure("delete app", err)
		return
	}
	log.Print("[sharing] deleted the workspace app: no chat is shared")
}

func state(ctx context.Context, sid, token string) State {
	st := State{Shared: db.IsSessionShared(sid), Participants: []Participant{}}
	present := map[string]bool{}
	for _, id := range chat.Present(sid) {
		present[id] = true
	}
	st.Present = len(present)
	guests, err := db.ListGuests(sid)
	if err != nil {
		log.Printf("[sharing] participants of %s: %v", sid, err)
	}
	for _, g := range guests {
		st.Participants = append(st.Participants, Participant{Guest: g, Present: present[g.ID]})
	}
	st.ShareOptions = db.SessionShareOptions(sid)
	cfg, _ := db.GetSessionConfig(sid)
	if dir, err := guest.FilesFolder(cfg.Workdir); err != nil {
		st.FilesUnavailable = err.Error()
	} else {
		st.FilesFolder = dir
	}
	if !st.Shared {
		return st
	}
	if token == "" {
		st.Problem = errNoToken.msg
		return st
	}
	app, ok, err := findApp(ctx, token)
	switch {
	case err != nil:
		st.Problem = err.Error()
	case !ok:
		st.Problem = "The link is not available: the workspace app for shared chats no longer exists. Stop sharing, then share again."
	default:
		st.ShareURL = strings.TrimRight(app.URL, "/") + "/s/" + sid
		st.Online = app.Online
	}
	return st
}

func sessionOr404(w http.ResponseWriter, r *http.Request) (string, bool) {
	sid := r.PathValue("session_id")
	if !db.SessionExists(sid) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return "", false
	}
	return sid, true
}

// HandleGet serves GET /api/sessions/{session_id}/share.
func HandleGet(w http.ResponseWriter, r *http.Request) {
	sid, ok := sessionOr404(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, state(r.Context(), sid, workspaceapps.OwnerToken(r)))
}

// HandleStart serves PUT /api/sessions/{session_id}/share. With no body it
// starts sharing: the app is created or confirmed before the chat is marked
// shared, so a platform error leaves the chat unshared. With a body it
// changes what guests may do in a chat already shared.
func HandleStart(w http.ResponseWriter, r *http.Request) {
	sid, ok := sessionOr404(w, r)
	if !ok {
		return
	}
	var change optionsChange
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err == nil && len(bytes.TrimSpace(raw)) > 0 {
		err = json.Unmarshal(raw, &change)
	}
	if err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if change.AllowPermissions != nil || change.AllowFiles != nil {
		changeOptions(w, r, sid, change)
		return
	}
	if _, up := guestPort(); !up {
		writeFailure(w, &failure{http.StatusServiceUnavailable, "Sharing is unavailable in this workspace."})
		return
	}
	token := workspaceapps.OwnerToken(r)
	if token == "" {
		writeFailure(w, errNoToken)
		return
	}
	appMu.Lock()
	_, err = ensureApp(r.Context(), token)
	if err == nil {
		err = db.SetSessionShared(sid, true)
	}
	appMu.Unlock()
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state(r.Context(), sid, token))
}

// optionsChange is PUT /share's body: only the options named change.
type optionsChange struct {
	AllowPermissions *bool `json:"allow_permissions"`
	AllowFiles       *bool `json:"allow_files"`
}

// optionsMu keeps two quick toggles from overwriting each other.
var optionsMu sync.Mutex

// changeOptions sets what guests may do in a shared chat. Local only: no
// platform call, so no token. Files are refused when the chat's folder would
// hand guests the owner's credentials; guests' pages hear of the change
// at once.
func changeOptions(w http.ResponseWriter, r *http.Request, sid string, change optionsChange) {
	optionsMu.Lock()
	o := db.SessionShareOptions(sid)
	if change.AllowPermissions != nil {
		o.AllowPermissions = *change.AllowPermissions
	}
	var err error
	if change.AllowFiles != nil {
		if *change.AllowFiles {
			cfg, _ := db.GetSessionConfig(sid)
			if _, why := guest.FilesFolder(cfg.Workdir); why != nil {
				err = &failure{http.StatusConflict, why.Error()}
			}
		}
		o.AllowFiles = *change.AllowFiles
	}
	if err == nil {
		var shared bool
		if shared, err = db.SetShareOptions(sid, o); err == nil && !shared {
			err = &failure{http.StatusConflict, "Share this chat first."}
		}
	}
	optionsMu.Unlock()
	if err != nil {
		writeFailure(w, err)
		return
	}
	chat.ShareOptionsChanged(sid)
	log.Printf("[sharing] %s: guests may answer=%t, see files=%t", sid, o.AllowPermissions, o.AllowFiles)
	writeJSON(w, http.StatusOK, state(r.Context(), sid, workspaceapps.OwnerToken(r)))
}

// HandleStop serves DELETE /api/sessions/{session_id}/share. The chat
// is unshared first, so Knowledge Worker Agent refuses guests whatever the platform does;
// then guests still connected are told, and the prompts they queued dropped.
// A turn already running continues.
func HandleStop(w http.ResponseWriter, r *http.Request) {
	sid, ok := sessionOr404(w, r)
	if !ok {
		return
	}
	if err := db.SetSessionShared(sid, false); err != nil {
		writeFailure(w, err)
		return
	}
	closed := chat.CloseViewers(sid, "sharing_ended", "")
	dropped := chat.DropGuestPrompts(sid)
	log.Printf("[sharing] stopped sharing %s: closed %d guest connections, dropped %d queued prompts", sid, closed, dropped)

	token := workspaceapps.OwnerToken(r)
	appMu.Lock()
	retireAppIfUnused(r.Context(), token)
	appMu.Unlock()
	writeJSON(w, http.StatusOK, state(r.Context(), sid, token))
}

// RetireAppAfter wraps a handler that can delete chats: when it leaves no chat
// shared, the app goes too, as when the last share stops.
func RetireAppAfter(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wasShared := len(db.SharedSessionIDs()) > 0
		next(w, r)
		if wasShared {
			appMu.Lock()
			retireAppIfUnused(r.Context(), workspaceapps.OwnerToken(r))
			appMu.Unlock()
		}
	}
}

// AudienceState is GET /api/share/audience.
type AudienceState struct {
	Members  []workspaceapps.Member `json:"members"`
	Audience db.Audience            `json:"audience"`
	// App is whether the app exists now; without one the audience is what the
	// next share will create it with.
	App bool `json:"app"`
}

// members returns the project's members other than the owner, who needs no
// invitation to their own workspace.
func members(ctx context.Context, token string) ([]workspaceapps.Member, error) {
	all, err := listMembers(ctx, token)
	if err != nil {
		return nil, platformFailure("list members", err)
	}
	out := []workspaceapps.Member{}
	for _, m := range all {
		if config.OwnerEmail != "" && strings.EqualFold(m.Email, config.OwnerEmail) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func audienceState(ctx context.Context, token string) (AudienceState, error) {
	ms, err := members(ctx, token)
	if err != nil {
		return AudienceState{}, err
	}
	appMu.Lock()
	defer appMu.Unlock()
	retireAppIfUnused(ctx, token)
	app, ok, err := findApp(ctx, token)
	if err != nil {
		return AudienceState{}, err
	}
	st := AudienceState{Members: ms, Audience: db.RememberedAudience(), App: ok}
	if ok {
		st.Audience = db.Audience{UserIDs: app.UserIDs, Project: app.IsSharedWithTeam}
		if st.Audience.UserIDs == nil {
			st.Audience.UserIDs = []uint64{}
		}
	}
	return st, nil
}

// HandleGetAudience serves GET /api/share/audience.
func HandleGetAudience(w http.ResponseWriter, r *http.Request) {
	token := workspaceapps.OwnerToken(r)
	if token == "" {
		writeFailure(w, errNoToken)
		return
	}
	st, err := audienceState(r.Context(), token)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// HandlePutAudience serves PUT /api/share/audience. Only project members can
// be chosen. The app, when there is one, is edited whole; the audience
// is remembered either way, for the next time the app is created.
func HandlePutAudience(w http.ResponseWriter, r *http.Request) {
	token := workspaceapps.OwnerToken(r)
	if token == "" {
		writeFailure(w, errNoToken)
		return
	}
	var want db.Audience
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&want); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	ms, err := members(r.Context(), token)
	if err != nil {
		writeFailure(w, err)
		return
	}
	known := map[uint64]bool{}
	for _, m := range ms {
		known[m.ID] = true
	}
	ids := []uint64{}
	seen := map[uint64]bool{}
	for _, id := range want.UserIDs {
		if !known[id] {
			writeFailure(w, &failure{http.StatusBadRequest, "Only members of this project can be added."})
			return
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	want.UserIDs = ids

	appMu.Lock()
	app, ok, err := findApp(r.Context(), token)
	if err == nil && ok {
		app.UserIDs, app.IsSharedWithTeam = want.UserIDs, want.Project
		if e := editApp(r.Context(), token, app); e != nil {
			err = platformFailure("edit app", e)
		}
	}
	if err == nil {
		err = db.RememberAudience(want)
	}
	appMu.Unlock()
	if err != nil {
		writeFailure(w, err)
		return
	}
	st, err := audienceState(r.Context(), token)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// Activity is one shared chat's live state, for the sidebar.
type Activity struct {
	SessionID string `json:"session_id"`
	Present   int    `json:"present"`
	Streaming bool   `json:"streaming"`
	Waiting   bool   `json:"waiting"`
}

// HandleActivity serves GET /api/share/activity: for every shared chat, who is
// here and whether a turn is running or waiting on the owner. The owner's tab
// follows only the chat it has open, so this is how the sidebar learns
// about the others. Local only; no platform call.
func HandleActivity(w http.ResponseWriter, r *http.Request) {
	out := []Activity{}
	for _, sid := range db.SharedSessionIDs() {
		out = append(out, Activity{
			SessionID: sid,
			Present:   len(chat.Present(sid)),
			Streaming: chat.IsStreaming(sid),
			Waiting:   chat.WaitingOnOwner(sid),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}
