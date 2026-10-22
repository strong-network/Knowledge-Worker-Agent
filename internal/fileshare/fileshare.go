// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package fileshare is the owner's side of sharing a single file from a chat:
// each shared file gets a port of its own and a Workspace App that publishes
// that port to the people the owner chose. Every platform call borrows the
// owner's token from the owner's own request, so the token is never kept.
// Coworkers reach the file through internal/fileshare/viewer.
package fileshare

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/fileshare/check"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/fileshare/viewer"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workspaceapps"
)

// appPrefix starts the name of every app this package creates. Only apps with
// it are ever deleted as leftovers, so an app the user made on a port in the
// range is left alone.
const appPrefix = "Artifact: "

const defaultPorts = "8770-8799"

// reserved are ports the platform refuses an app on.
var reserved = map[int]bool{1111: true, 2376: true, 11111: true, 11112: true}

// The platform, as variables so tests can stand in.
var (
	listApps    = workspaceapps.List
	createApp   = workspaceapps.Create
	editApp     = workspaceapps.Edit
	deleteApp   = workspaceapps.Delete
	listMembers = workspaceapps.Members
)

// mu serialises port and app changes, so two requests can't take one port.
var mu sync.Mutex

type failure struct {
	status int
	msg    string
}

func (f *failure) Error() string { return f.msg }

var errNoToken = &failure{http.StatusForbidden, "Open Knowledge Worker Agent from your workspace to share files. This request did not carry your workspace credential."}

// platformFailure turns a sidecar error into what the owner is told. The
// platform's own message is shown, never logged.
func platformFailure(action string, err error) *failure {
	return platformFailureAs("Sharing could not be set up: ", action, err)
}

func platformFailureAs(prefix, action string, err error) *failure {
	log.Printf("[fileshare] %s failed: %v", action, err)
	var se *workspaceapps.Error
	if errors.As(err, &se) && se.Message != "" {
		return &failure{http.StatusBadGateway, prefix + se.Message}
	}
	return &failure{http.StatusBadGateway, prefix + "the workspace platform did not answer."}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeFailure never answers 503: the platform proxy empties a 503's body.
func writeFailure(w http.ResponseWriter, err error) {
	var f *failure
	if !errors.As(err, &f) {
		log.Printf("[fileshare] %v", err)
		f = &failure{http.StatusInternalServerError, "Sharing could not be changed."}
	}
	writeJSON(w, f.status, map[string]string{"error": f.msg})
}

// ports reads KWA_ARTIFACT_PORTS, a port or a range such as 8770-8799. "off"
// turns sharing files off and gives no ports.
func ports() ([]int, error) {
	v := strings.ToLower(strings.TrimSpace(env.Get("KWA_ARTIFACT_PORTS")))
	switch v {
	case "":
		v = defaultPorts
	case "off", "false", "no", "0":
		return nil, nil
	}
	loS, hiS, isRange := strings.Cut(v, "-")
	if !isRange {
		hiS = loS
	}
	lo, err1 := strconv.Atoi(strings.TrimSpace(loS))
	hi, err2 := strconv.Atoi(strings.TrimSpace(hiS))
	if err1 != nil || err2 != nil || lo < 1 || hi > 65535 || lo > hi || hi-lo >= 1000 {
		return nil, fmt.Errorf("KWA_ARTIFACT_PORTS=%q is not a port or a range of ports", v)
	}
	out := []int{}
	for p := lo; p <= hi; p++ {
		if !reserved[p] {
			out = append(out, p)
		}
	}
	return out, nil
}

// enabled reports whether sharing files is on: KWA_ARTIFACT_PORTS gives ports.
func enabled() bool {
	ps, err := ports()
	return err == nil && len(ps) > 0
}

// Start serves every shared file at startup, unless sharing files is turned
// off: then none is served, and each is served again once it's back on. Apps
// live on the platform, so this needs no token; a port something else took
// meanwhile leaves its file unavailable until the owner repairs it.
func Start() {
	if n, err := db.FailRunningFixes("The fix stopped when Knowledge Worker Agent restarted."); err != nil {
		log.Printf("[fileshare] end fixes left running: %v", err)
	} else if n > 0 {
		log.Printf("[fileshare] %d fixes were running when the server stopped; marked failed", n)
	}
	arts := db.SharedArtifacts()
	if !enabled() {
		if len(arts) > 0 {
			log.Printf("[fileshare] sharing files is turned off (KWA_ARTIFACT_PORTS), so %d shared files aren't served", len(arts))
		}
		return
	}
	for _, a := range arts {
		if err := viewer.Bind(a.ID, a.Port); err != nil {
			log.Printf("[fileshare] cannot serve %s on port %d: %v", a.RelPath, a.Port, err)
			continue
		}
		log.Printf("[fileshare] serving %s on 127.0.0.1:%d", a.RelPath, a.Port)
	}
}

// Artifact is a shared file as the owner sees it.
type Artifact struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	SessionID     string `json:"session_id"`
	ChatLabel     string `json:"chat_label"`
	URL           string `json:"url"`
	Port          int    `json:"port"`
	AllowDownload bool   `json:"allow_download"`
	SharedAt      string `json:"shared_at"`
	// Updated and Size are the file's, "" and 0 when it can't be read.
	Updated string `json:"updated"`
	Size    int64  `json:"size"`
	// Status is shared, off (sharing files is turned off, so it isn't served),
	// missing (the file can't be read), unavailable (its port is taken),
	// removed (its app was deleted outside Knowledge Worker Agent), attention
	// (it is no longer self-contained) or fixing (a fix is running for it).
	// Problem says why, in words, when it isn't shared.
	Status  string `json:"status"`
	Problem string `json:"problem,omitempty"`
	Online  bool   `json:"online"`
	// Findings are the self-contained check's, for the file as it is now.
	Findings []check.Finding `json:"findings"`
	// Audience is nil when the platform wasn't asked: the request carried no token.
	Audience *db.Audience `json:"audience"`
}

// view describes a. apps is the workspace's apps by port, nil when the platform
// wasn't asked.
func view(a db.SharedArtifact, apps map[int]workspaceapps.App) Artifact {
	out := Artifact{
		ID: a.ID, Name: filepath.Base(a.RelPath), Path: filepath.Join(a.Folder, a.RelPath),
		SessionID: a.SessionID, URL: viewer.Origin(a.Port) + "/", Port: a.Port,
		AllowDownload: a.AllowDownload, SharedAt: a.SharedAt, Status: "shared",
	}
	if a.SessionID != "" {
		if cfg, err := db.GetSessionConfig(a.SessionID); err == nil {
			out.ChatLabel = cfg.Label
		}
	}
	res := check.Result{Findings: []check.Finding{}}
	f, info, err := viewer.Open(a.Folder, a.RelPath)
	if err == nil {
		out.Updated, out.Size = info.ModTime().UTC().Format(time.RFC3339), info.Size()
		if checkKey(info) != a.CheckKey || json.Unmarshal([]byte(a.CheckJSON), &res) != nil {
			res = recheck(a.ID, f, info, a.RelPath)
		}
		f.Close()
	}
	out.Findings = res.Findings
	app, hasApp := apps[a.Port]
	hasApp = hasApp && strings.HasPrefix(app.Name, appPrefix)
	switch {
	case !enabled():
		out.Status, out.Problem = "off", "Sharing files is turned off in this workspace, so nobody can open it. It opens again when sharing is turned back on."
	case err != nil:
		out.Status, out.Problem = "missing", err.Error()
	case !viewer.Bound(a.Port):
		out.Status, out.Problem = "unavailable", fmt.Sprintf("Port %d is in use by something else, so the file can't be opened. Repair it to move it to a free port; its link will change.", a.Port)
	case apps != nil && !hasApp:
		out.Status, out.Problem = "removed", "Sharing was removed outside Knowledge Worker Agent. Repair it to share the file again."
	case res.Blocking:
		out.Status, out.Problem = "attention", "Parts of it no longer work for viewers: it now loads things from the internet or from other files, which their browsers block."
	}
	if out.Status == "shared" || out.Status == "attention" {
		if _, fixing := db.RunningFixAt(a.Folder, a.RelPath); fixing {
			out.Status, out.Problem = "fixing", "It's being made self-contained in the background."
		}
	}
	if hasApp {
		if app.URL != "" {
			out.URL = strings.TrimRight(app.URL, "/") + "/"
		}
		out.Online = app.Online
		ids := app.UserIDs
		if ids == nil {
			ids = []uint64{}
		}
		out.Audience = &db.Audience{UserIDs: ids, Project: app.IsSharedWithTeam}
	}
	return out
}

// checkKey is what a cached check is valid for: the file's size and time.
func checkKey(info fs.FileInfo) string {
	return fmt.Sprintf("%d|%d", info.Size(), info.ModTime().UnixNano())
}

// checked reads the open file and checks it.
func checked(f *os.File, rel string) (check.Result, error) {
	data, err := io.ReadAll(f)
	if err != nil {
		return check.Result{Findings: []check.Finding{}}, err
	}
	return check.File(rel, data), nil
}

// recheck checks a shared file again and keeps the result for its key. A read
// error keeps nothing, so the next look tries again.
func recheck(id string, f *os.File, info fs.FileInfo, rel string) check.Result {
	res, err := checked(f, rel)
	if err != nil {
		return res
	}
	if b, err := json.Marshal(res); err == nil {
		if err := db.SetArtifactCheck(id, checkKey(info), string(b)); err != nil {
			log.Printf("[fileshare] keep the check of %s: %v", rel, err)
		}
	}
	return res
}

const notSelfContained = "This file isn't self-contained, so parts of it would be blocked for the people you share it with."

type fileRequest struct {
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
}

// HandleCheck serves POST /api/artifacts/check: what in a file wouldn't work
// for viewers, before it is shared.
func HandleCheck(w http.ResponseWriter, r *http.Request) {
	var req fileRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	folder, rel, err := resolve(req.SessionID, req.Path)
	if err != nil {
		writeFailure(w, err)
		return
	}
	f, _, err := viewer.Open(folder, rel)
	if err != nil {
		writeFailure(w, &failure{http.StatusConflict, err.Error()})
		return
	}
	res, err := checked(f, rel)
	f.Close()
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"self_contained": !res.Blocking, "findings": res.Findings})
}

func byPort(apps []workspaceapps.App) map[int]workspaceapps.App {
	out := make(map[int]workspaceapps.App, len(apps))
	for _, a := range apps {
		out[int(a.Port)] = a
	}
	return out
}

// appsByPort asks the platform for the workspace's apps, when there's a token.
func appsByPort(ctx context.Context, token string) (map[int]workspaceapps.App, error) {
	if token == "" {
		return nil, nil
	}
	all, err := listApps(ctx, token)
	if err != nil {
		return nil, platformFailure("list apps", err)
	}
	return byPort(all), nil
}

// retireLeftovers deletes apps this package made whose file is no longer
// shared: what a stop that couldn't reach the platform left behind.
func retireLeftovers(ctx context.Context, token string, apps map[int]workspaceapps.App) {
	inRange := map[int]bool{}
	ps, _ := ports()
	for _, p := range ps {
		inRange[p] = true
	}
	used := map[int]bool{}
	for _, a := range db.SharedArtifacts() {
		used[a.Port] = true
	}
	for port, app := range apps {
		if !strings.HasPrefix(app.Name, appPrefix) || !inRange[port] || used[port] {
			continue
		}
		if err := deleteApp(ctx, token, app.ID); err != nil {
			platformFailure("delete leftover app", err)
			continue
		}
		delete(apps, port)
		log.Printf("[fileshare] deleted the leftover app on port %d", port)
	}
}

// renameApps gives a shared file's app its file's current name, so the people
// it's shared with see that name on the platform too. A rename in the Files
// widget changes only the file's path; the widget lists again after it.
func renameApps(ctx context.Context, token string, apps map[int]workspaceapps.App) {
	for _, a := range db.SharedArtifacts() {
		app, ok := apps[a.Port]
		if !ok || !strings.HasPrefix(app.Name, appPrefix) || app.Name == appName(a.RelPath) {
			continue
		}
		app.Name = appName(a.RelPath)
		if err := editApp(ctx, token, app); err != nil {
			platformFailure("rename app", err)
			continue
		}
		apps[a.Port] = app
		log.Printf("[fileshare] renamed the app on port %d to %q", a.Port, app.Name)
	}
}

type listResponse struct {
	Artifacts []Artifact `json:"artifacts"`
	// Enabled is false when KWA_ARTIFACT_PORTS turns sharing files off.
	Enabled bool `json:"enabled"`
	// Limit is how many files can be shared at once.
	Limit   int    `json:"limit"`
	Problem string `json:"problem,omitempty"`
	// Fixes are the fixes running, shared files or not.
	Fixes []Fix `json:"fixes"`
}

// HandleList serves GET /api/artifacts: every shared file. With the owner's
// token it also asks the platform who can open each one, and deletes apps
// whose file is no longer shared.
func HandleList(w http.ResponseWriter, r *http.Request) {
	ps, err := ports()
	resp := listResponse{Artifacts: []Artifact{}, Enabled: err == nil && len(ps) > 0, Limit: len(ps), Fixes: []Fix{}}
	if err != nil {
		resp.Problem = err.Error()
	}
	token := workspaceapps.OwnerToken(r)
	mu.Lock()
	apps, perr := appsByPort(r.Context(), token)
	if perr == nil && apps != nil {
		retireLeftovers(r.Context(), token, apps)
		renameApps(r.Context(), token, apps)
	}
	arts := db.SharedArtifacts()
	mu.Unlock()
	switch {
	case perr != nil:
		resp.Problem = perr.Error()
	case token == "" && len(arts) > 0:
		resp.Problem = "Open Knowledge Worker Agent from your workspace to see who can open your files."
	}
	for _, a := range arts {
		resp.Artifacts = append(resp.Artifacts, view(a, apps))
	}
	for _, f := range db.RunningFixes() {
		resp.Fixes = append(resp.Fixes, fixView(f))
	}
	writeJSON(w, http.StatusOK, resp)
}

// members returns the project's members other than the owner, who needs no
// invitation to their own workspace's apps.
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

// HandleMembers serves GET /api/artifacts/members: who a file can be shared with.
func HandleMembers(w http.ResponseWriter, r *http.Request) {
	token := workspaceapps.OwnerToken(r)
	if token == "" {
		writeFailure(w, errNoToken)
		return
	}
	ms, err := members(r.Context(), token)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": ms})
}

// checkAudience keeps only project members, once each.
func checkAudience(ctx context.Context, token string, a db.Audience) (db.Audience, error) {
	out := db.Audience{Project: a.Project, UserIDs: []uint64{}}
	if len(a.UserIDs) == 0 {
		return out, nil
	}
	ms, err := members(ctx, token)
	if err != nil {
		return out, err
	}
	known := map[uint64]bool{}
	for _, m := range ms {
		known[m.ID] = true
	}
	seen := map[uint64]bool{}
	for _, id := range a.UserIDs {
		if !known[id] {
			return out, &failure{http.StatusBadRequest, "Only members of this project can be added."}
		}
		if !seen[id] {
			seen[id] = true
			out.UserIDs = append(out.UserIDs, id)
		}
	}
	return out, nil
}

// resolve finds the chat's folder and the file's path inside it, refusing a
// temporary chat (it's deleted after a while) and a path outside the folder.
func resolve(sessionID, path string) (folder, rel string, err error) {
	if !db.SessionExists(sessionID) {
		return "", "", &failure{http.StatusNotFound, "Chat not found."}
	}
	cfg, err := db.GetSessionConfig(sessionID)
	if err != nil {
		return "", "", err
	}
	if cfg.Temporary {
		return "", "", &failure{http.StatusConflict, "Files in a temporary chat can't be shared. Keep the chat first."}
	}
	if strings.TrimSpace(cfg.Workdir) == "" || !filepath.IsAbs(path) {
		return "", "", &failure{http.StatusBadRequest, "Only files in this chat's folder can be shared."}
	}
	folder = filepath.Clean(cfg.Workdir)
	rel, err = filepath.Rel(folder, filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", &failure{http.StatusBadRequest, "Only files in this chat's folder can be shared."}
	}
	return folder, rel, nil
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func appName(rel string) string {
	name := []rune(filepath.Base(rel))
	if len(name) > 80 {
		name = append(name[:79], '…')
	}
	return appPrefix + string(name)
}

// bindFree serves id on the lowest port in the range that has no app and is
// free on this machine.
func bindFree(id string, apps map[int]workspaceapps.App) (int, error) {
	ps, err := ports()
	if err != nil {
		return 0, &failure{http.StatusConflict, err.Error()}
	}
	if len(ps) == 0 {
		return 0, &failure{http.StatusConflict, "Sharing files is turned off in this workspace."}
	}
	for _, p := range ps {
		if _, taken := apps[p]; taken || viewer.Bound(p) {
			continue
		}
		if viewer.Bind(id, p) == nil {
			return p, nil
		}
	}
	return 0, &failure{http.StatusConflict, fmt.Sprintf("All %d places for shared files are taken. Stop sharing another file first.", len(ps))}
}

type shareRequest struct {
	SessionID     string      `json:"session_id"`
	Path          string      `json:"path"`
	Audience      db.Audience `json:"audience"`
	AllowDownload bool        `json:"allow_download"`
}

// HandleShare serves POST /api/artifacts: share a file from a chat. The port
// is bound and the app created before the file is recorded as shared, so a
// failure on the way leaves nothing shared.
func HandleShare(w http.ResponseWriter, r *http.Request) {
	var req shareRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	folder, rel, err := resolve(req.SessionID, req.Path)
	if err != nil {
		writeFailure(w, err)
		return
	}
	f, info, err := viewer.Open(folder, rel)
	if err != nil {
		writeFailure(w, &failure{http.StatusConflict, err.Error()})
		return
	}
	res, err := checked(f, rel)
	f.Close()
	if err != nil {
		writeFailure(w, err)
		return
	}
	if res.Blocking {
		writeJSON(w, http.StatusConflict, map[string]any{"error": notSelfContained, "findings": res.Findings})
		return
	}
	token := workspaceapps.OwnerToken(r)
	if token == "" {
		writeFailure(w, errNoToken)
		return
	}
	aud, err := checkAudience(r.Context(), token, req.Audience)
	if err != nil {
		writeFailure(w, err)
		return
	}

	mu.Lock()
	defer mu.Unlock()
	if _, shared := db.SharedArtifactAt(folder, rel); shared {
		writeFailure(w, &failure{http.StatusConflict, "This file is already shared."})
		return
	}
	apps, err := appsByPort(r.Context(), token)
	if err != nil {
		writeFailure(w, err)
		return
	}
	a := db.SharedArtifact{ID: newID(), Folder: folder, RelPath: rel, SessionID: req.SessionID, AllowDownload: req.AllowDownload}
	if a.Port, err = bindFree(a.ID, apps); err != nil {
		writeFailure(w, err)
		return
	}
	app, err := createApp(r.Context(), token, uint32(a.Port), appName(rel), aud.Project, aud.UserIDs)
	if err != nil {
		viewer.Unbind(a.Port)
		writeFailure(w, platformFailure("create app", err))
		return
	}
	if err := db.CreateSharedArtifact(a); err != nil {
		if e := deleteApp(r.Context(), token, app.ID); e != nil {
			platformFailure("delete app", e)
		}
		viewer.Unbind(a.Port)
		writeFailure(w, err)
		return
	}
	if b, err := json.Marshal(res); err == nil {
		_ = db.SetArtifactCheck(a.ID, checkKey(info), string(b))
	}
	log.Printf("[fileshare] shared %s on port %d", rel, a.Port)
	// The create reply omits the audience; it is what was asked for.
	app.Name, app.Port = appName(rel), uint32(a.Port)
	app.UserIDs, app.IsSharedWithTeam = aud.UserIDs, aud.Project
	stored, _ := db.GetSharedArtifact(a.ID)
	writeJSON(w, http.StatusOK, view(stored, map[int]workspaceapps.App{a.Port: app}))
}

func artifactOr404(w http.ResponseWriter, r *http.Request) (db.SharedArtifact, bool) {
	a, ok := db.GetSharedArtifact(r.PathValue("id"))
	if !ok {
		writeFailure(w, &failure{http.StatusNotFound, "This file isn't shared."})
	}
	return a, ok
}

// respond writes a's view, asking the platform when the request carries a token.
func respond(w http.ResponseWriter, r *http.Request, id string) {
	a, ok := db.GetSharedArtifact(id)
	if !ok {
		writeFailure(w, &failure{http.StatusNotFound, "This file isn't shared."})
		return
	}
	apps, err := appsByPort(r.Context(), workspaceapps.OwnerToken(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view(a, apps))
}

// HandleAudience serves PUT /api/artifacts/{id}/audience: who can open the
// file. The app is listed and sent back whole: an edit replaces every field.
func HandleAudience(w http.ResponseWriter, r *http.Request) {
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
	aud, err := checkAudience(r.Context(), token, want)
	if err != nil {
		writeFailure(w, err)
		return
	}
	mu.Lock()
	a, ok := artifactOr404(w, r)
	if !ok {
		mu.Unlock()
		return
	}
	apps, err := appsByPort(r.Context(), token)
	if err == nil {
		app, has := apps[a.Port]
		if !has || !strings.HasPrefix(app.Name, appPrefix) {
			err = &failure{http.StatusConflict, "Sharing was removed outside Knowledge Worker Agent. Repair it to share the file again."}
		} else {
			app.UserIDs, app.IsSharedWithTeam = aud.UserIDs, aud.Project
			if e := editApp(r.Context(), token, app); e != nil {
				err = platformFailure("edit app", e)
			}
		}
	}
	mu.Unlock()
	if err != nil {
		writeFailure(w, err)
		return
	}
	respond(w, r, a.ID)
}

// HandleOptions serves PUT /api/artifacts/{id}/options: whether viewers can
// download the file. Local only.
func HandleOptions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AllowDownload *bool `json:"allow_download"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.AllowDownload == nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	a, ok := artifactOr404(w, r)
	if !ok {
		return
	}
	if err := db.SetSharedArtifactDownload(a.ID, *body.AllowDownload); err != nil {
		writeFailure(w, err)
		return
	}
	respond(w, r, a.ID)
}

// stop stops sharing files: each is forgotten and its port closed first, so
// viewers are refused whatever the platform does; then their apps are deleted.
// One that can't be deleted now is deleted on a later list.
func stop(ctx context.Context, token string, arts []db.SharedArtifact) {
	for _, a := range arts {
		if err := db.DeleteSharedArtifact(a.ID); err != nil {
			log.Printf("[fileshare] forget %s: %v", a.RelPath, err)
		}
		viewer.Unbind(a.Port)
		log.Printf("[fileshare] stopped sharing %s (port %d)", a.RelPath, a.Port)
	}
	apps, err := appsByPort(ctx, token)
	if err != nil || apps == nil {
		return
	}
	for _, a := range arts {
		if app, ok := apps[a.Port]; ok && strings.HasPrefix(app.Name, appPrefix) {
			if err := deleteApp(ctx, token, app.ID); err != nil {
				platformFailure("delete app", err)
			}
		}
	}
}

// HandleStop serves DELETE /api/artifacts/{id}: stop sharing the file.
func HandleStop(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	a, ok := artifactOr404(w, r)
	if !ok {
		return
	}
	stop(r.Context(), workspaceapps.OwnerToken(r), []db.SharedArtifact{a})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// HandleRepair serves POST /api/artifacts/{id}/repair. A file whose app was
// removed outside Knowledge Worker Agent gets a new one on the same port, so
// its link stays, shared with no one until the owner chooses. A file whose
// port is taken moves to a free one, keeping who can open it; its link changes.
func HandleRepair(w http.ResponseWriter, r *http.Request) {
	token := workspaceapps.OwnerToken(r)
	if token == "" {
		writeFailure(w, errNoToken)
		return
	}
	mu.Lock()
	err := repair(r.Context(), token, r.PathValue("id"))
	mu.Unlock()
	if err != nil {
		writeFailure(w, err)
		return
	}
	respond(w, r, r.PathValue("id"))
}

func repair(ctx context.Context, token, id string) error {
	if !enabled() {
		return &failure{http.StatusConflict, "Sharing files is turned off in this workspace."}
	}
	a, ok := db.GetSharedArtifact(id)
	if !ok {
		return &failure{http.StatusNotFound, "This file isn't shared."}
	}
	apps, err := appsByPort(ctx, token)
	if err != nil {
		return err
	}
	old, hadApp := apps[a.Port]
	ours := hadApp && strings.HasPrefix(old.Name, appPrefix)
	bound := viewer.Bound(a.Port) || viewer.Bind(a.ID, a.Port) == nil
	switch {
	case bound && ours:
		return nil
	case bound && !hadApp:
		if _, err := createApp(ctx, token, uint32(a.Port), appName(a.RelPath), false, nil); err != nil {
			return platformFailure("create app", err)
		}
		log.Printf("[fileshare] repaired %s: new app on port %d", a.RelPath, a.Port)
		return nil
	}
	// The port is taken, by another program or by an app that isn't ours: move.
	if bound {
		viewer.Unbind(a.Port)
	}
	port, err := bindFree(a.ID, apps)
	if err != nil {
		return err
	}
	aud := db.Audience{UserIDs: []uint64{}}
	if ours {
		aud = db.Audience{UserIDs: old.UserIDs, Project: old.IsSharedWithTeam}
	}
	if _, err := createApp(ctx, token, uint32(port), appName(a.RelPath), aud.Project, aud.UserIDs); err != nil {
		viewer.Unbind(port)
		return platformFailure("create app", err)
	}
	if err := db.SetSharedArtifactPort(a.ID, port); err != nil {
		viewer.Unbind(port)
		return err
	}
	if ours {
		if err := deleteApp(ctx, token, old.ID); err != nil {
			platformFailure("delete app", err)
		}
	}
	log.Printf("[fileshare] repaired %s: moved from port %d to %d", a.RelPath, a.Port, port)
	return nil
}

// AfterChatsDeleted wraps a handler that can delete chats. A file shared from
// a deleted chat stops being shared when its folder went with the chat; when
// the folder stays (a project's, a task's, one the user chose), the file stays
// shared and only the link to the chat is dropped.
func AfterChatsDeleted(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next(w, r)
		var gone []db.SharedArtifact
		for _, a := range db.SharedArtifacts() {
			if a.SessionID == "" || db.SessionExists(a.SessionID) {
				continue
			}
			if _, err := os.Stat(a.Folder); err == nil {
				if err := db.ClearArtifactSession(a.ID); err != nil {
					log.Printf("[fileshare] %s: %v", a.RelPath, err)
				}
				continue
			}
			gone = append(gone, a)
		}
		if len(gone) == 0 {
			return
		}
		mu.Lock()
		stop(r.Context(), workspaceapps.OwnerToken(r), gone)
		mu.Unlock()
	}
}

// inside reports whether path is dir or inside it, and its path below dir.
func inside(dir, path string) (string, bool) {
	if path == dir {
		return "", true
	}
	if rest, ok := strings.CutPrefix(path, dir+string(filepath.Separator)); ok {
		return rest, true
	}
	return "", false
}

// Moved follows a rename or move in the owner's file routes: a file shared at
// src, or inside a folder at src, stays shared at its new place. One moved out
// of its chat's folder can't follow, and shows as missing.
func Moved(src, dest string) {
	mu.Lock()
	defer mu.Unlock()
	for _, a := range db.SharedArtifacts() {
		rest, ok := inside(src, filepath.Join(a.Folder, a.RelPath))
		if !ok {
			continue
		}
		rel, err := filepath.Rel(a.Folder, filepath.Join(dest, rest))
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			log.Printf("[fileshare] %s left its chat's folder, so it can't be found", a.RelPath)
			continue
		}
		if err := db.SetSharedArtifactPath(a.ID, rel); err != nil {
			log.Printf("[fileshare] follow %s to %s: %v", a.RelPath, rel, err)
			continue
		}
		log.Printf("[fileshare] %s is now shared as %s", a.RelPath, rel)
	}
}

// Deleting stops sharing what is at path, or inside it, before it's deleted,
// in the owner's request: it carries the token that deletes the apps.
func Deleting(r *http.Request, path string) {
	var gone []db.SharedArtifact
	for _, a := range db.SharedArtifacts() {
		if _, ok := inside(path, filepath.Join(a.Folder, a.RelPath)); ok {
			gone = append(gone, a)
		}
	}
	if len(gone) == 0 {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	stop(r.Context(), workspaceapps.OwnerToken(r), gone)
}
