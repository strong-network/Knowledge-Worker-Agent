// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sharing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workspaceapps"
)

const token = "tok-SECRET-owner"

// platform stands in for the sidecar: one workspace's apps and members.
type platform struct {
	mu        sync.Mutex
	apps      []workspaceapps.App
	members   []workspaceapps.Member
	calls     []string
	failWith  error
	nextID    uint64
	lastEdit  workspaceapps.App
	badTokens int
}

func (p *platform) note(call, tok string) error {
	p.calls = append(p.calls, call)
	if tok != token {
		p.badTokens++
	}
	return p.failWith
}

func fakePlatform(t *testing.T) *platform {
	t.Helper()
	if err := db.Init(filepath.Join(t.TempDir(), "t.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	p := &platform{nextID: 100, members: []workspaceapps.Member{
		{ID: 1, Email: "owner@example.com"}, {ID: 2, Email: "sarah@example.com"}, {ID: 3, Email: "tom@example.com"},
	}}
	prev := []any{listApps, createApp, editApp, deleteApp, listMembers, guestPort, config.OwnerEmail}
	t.Cleanup(func() {
		listApps = prev[0].(func(context.Context, string) ([]workspaceapps.App, error))
		createApp = prev[1].(func(context.Context, string, uint32, string, bool, []uint64) (workspaceapps.App, error))
		editApp = prev[2].(func(context.Context, string, workspaceapps.App) error)
		deleteApp = prev[3].(func(context.Context, string, uint64) error)
		listMembers = prev[4].(func(context.Context, string) ([]workspaceapps.Member, error))
		guestPort = prev[5].(func() (int, bool))
		config.OwnerEmail = prev[6].(string)
	})
	config.OwnerEmail = "Owner@Example.com"
	guestPort = func() (int, bool) { return 8766, true }
	listApps = func(_ context.Context, tok string) ([]workspaceapps.App, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if err := p.note("list", tok); err != nil {
			return nil, err
		}
		return append([]workspaceapps.App(nil), p.apps...), nil
	}
	createApp = func(_ context.Context, tok string, port uint32, name string, project bool, ids []uint64) (workspaceapps.App, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if err := p.note("create", tok); err != nil {
			return workspaceapps.App{}, err
		}
		p.nextID++
		a := workspaceapps.App{ID: p.nextID, Port: port, Name: name, URL: "https://ws-7-port-8766.proxy.example", Online: true, UserIDs: ids, IsSharedWithTeam: project}
		p.apps = append(p.apps, a)
		return workspaceapps.App{ID: a.ID, Port: port, Name: name, URL: a.URL, Online: true}, nil
	}
	editApp = func(_ context.Context, tok string, a workspaceapps.App) error {
		p.mu.Lock()
		defer p.mu.Unlock()
		if err := p.note("edit", tok); err != nil {
			return err
		}
		p.lastEdit = a
		for i := range p.apps {
			if p.apps[i].ID == a.ID {
				p.apps[i] = a
			}
		}
		return nil
	}
	deleteApp = func(_ context.Context, tok string, id uint64) error {
		p.mu.Lock()
		defer p.mu.Unlock()
		if err := p.note("delete", tok); err != nil {
			return err
		}
		kept := p.apps[:0]
		for _, a := range p.apps {
			if a.ID != id {
				kept = append(kept, a)
			}
		}
		p.apps = kept
		return nil
	}
	listMembers = func(_ context.Context, tok string) ([]workspaceapps.Member, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if err := p.note("members", tok); err != nil {
			return nil, err
		}
		return p.members, nil
	}
	return p
}

func (p *platform) count(call string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.calls {
		if c == call {
			n++
		}
	}
	return n
}

func routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{session_id}/share", HandleGet)
	mux.HandleFunc("PUT /api/sessions/{session_id}/share", HandleStart)
	mux.HandleFunc("DELETE /api/sessions/{session_id}/share", HandleStop)
	mux.HandleFunc("GET /api/share/audience", HandleGetAudience)
	mux.HandleFunc("PUT /api/share/audience", HandlePutAudience)
	mux.HandleFunc("GET /api/share/activity", HandleActivity)
	mux.HandleFunc("DELETE /api/sessions/{session_id}", RetireAppAfter(func(w http.ResponseWriter, r *http.Request) {
		db.DeleteSession(r.PathValue("session_id"))
	}))
	return mux
}

func do(t *testing.T, method, path, body string, withToken bool) (int, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if withToken {
		req.Header.Set("cloud_editor_user_token", token)
	}
	w := httptest.NewRecorder()
	routes().ServeHTTP(w, req)
	var m map[string]any
	json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m, w.Body.String()
}

func newChat(t *testing.T, id string) {
	t.Helper()
	if err := db.CreateSession(id, config.SessionConfig{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { chat.Streams.Delete(id); chat.ClearQueue(id) })
}

// The app is made before the chat is marked shared, with the remembered
// audience, and the link is the app's own URL plus the chat's path.
func TestStartCreatesTheAppThenShares(t *testing.T) {
	p := fakePlatform(t)
	newChat(t, "c1")
	db.RememberAudience(db.Audience{UserIDs: []uint64{2}})

	code, st, _ := do(t, "PUT", "/api/sessions/c1/share", "", true)
	if code != 200 || st["shared"] != true {
		t.Fatalf("start = %d %v", code, st)
	}
	if st["share_url"] != "https://ws-7-port-8766.proxy.example/s/c1" || st["online"] != true {
		t.Errorf("link = %v online = %v", st["share_url"], st["online"])
	}
	if len(p.apps) != 1 || p.apps[0].Port != 8766 || p.apps[0].IsPublic || len(p.apps[0].UserIDs) != 1 || p.apps[0].UserIDs[0] != 2 {
		t.Errorf("app created as %+v", p.apps)
	}
	// A second chat adopts the same app.
	newChat(t, "c2")
	do(t, "PUT", "/api/sessions/c2/share", "", true)
	if p.count("create") != 1 {
		t.Errorf("created %d apps for two chats", p.count("create"))
	}
	if p.badTokens != 0 {
		t.Errorf("%d platform calls without the owner's token", p.badTokens)
	}
}

func TestStartFailsSafe(t *testing.T) {
	p := fakePlatform(t)
	newChat(t, "c1")
	if code, _, _ := do(t, "PUT", "/api/sessions/c1/share", "", false); code != 403 || db.IsSessionShared("c1") || len(p.calls) != 0 {
		t.Errorf("without the token: %d shared=%v calls=%v", code, db.IsSessionShared("c1"), p.calls)
	}
	p.failWith = &workspaceapps.Error{Status: 400, Code: 3, Message: "reserved port"}
	code, body, _ := do(t, "PUT", "/api/sessions/c1/share", "", true)
	if code != 502 || db.IsSessionShared("c1") || !strings.Contains(body["error"].(string), "reserved port") {
		t.Errorf("platform refusal: %d %v shared=%v", code, body, db.IsSessionShared("c1"))
	}
	p.failWith = nil
	guestPort = func() (int, bool) { return 8766, false }
	if code, _, _ := do(t, "PUT", "/api/sessions/c1/share", "", true); code != 503 || db.IsSessionShared("c1") {
		t.Errorf("guest listener down: %d", code)
	}
}

// Stopping reaches guests already connected, drops what they queued,
// keeps the owner's, and the last share takes the app with it.
func TestStopRevokesAndRetiresTheLastApp(t *testing.T) {
	p := fakePlatform(t)
	newChat(t, "c1")
	newChat(t, "c2")
	do(t, "PUT", "/api/sessions/c1/share", "", true)
	do(t, "PUT", "/api/sessions/c2/share", "", true)
	do(t, "PUT", "/api/share/audience", `{"user_ids":[3],"project":true}`, true)

	guestView := chat.AddViewer("c1", "g-1")
	ownerView := chat.AddViewer("c1", "")
	defer chat.RemoveViewer("c1", guestView)
	defer chat.RemoveViewer("c1", ownerView)
	chat.OpenTurn("c1") // running, so prompts queue
	chat.EnqueuePrompt("c1", "from the owner")
	chat.EnqueuePrompt("c1", "from sarah", chat.Turn{Author: chat.Author{ID: "g-1", Name: "Sarah"}})

	if code, st, _ := do(t, "DELETE", "/api/sessions/c1/share", "", true); code != 200 || st["shared"] != false {
		t.Fatalf("stop = %d %v", code, st)
	}
	if guestView.Reason() != "sharing_ended" || ownerView.Reason() != "" {
		t.Errorf("guest reason %q, owner reason %q", guestView.Reason(), ownerView.Reason())
	}
	if q := chat.SnapshotQueue("c1"); len(q) != 1 || q[0].Prompt != "from the owner" {
		t.Errorf("queue after stop: %+v", q)
	}
	if p.count("delete") != 0 || len(p.apps) != 1 {
		t.Error("the app went while another chat is still shared")
	}
	// The audience is common to every shared chat, so c2's is untouched.
	if a := p.apps[0]; !a.IsSharedWithTeam || len(a.UserIDs) != 1 || a.UserIDs[0] != 3 {
		t.Errorf("stopping one of two shares changed the app's audience: %+v", a)
	}

	do(t, "DELETE", "/api/sessions/c2/share", "", true)
	if len(p.apps) != 0 || p.count("delete") != 1 {
		t.Errorf("the last stop left apps %+v", p.apps)
	}
	// The last stop forgets who could open shared chats: the modal must not
	// offer them again, and the next share starts from nobody.
	if a := db.RememberedAudience(); a.Project || len(a.UserIDs) != 0 {
		t.Errorf("audience kept after the last share stopped: %+v", a)
	}
	if _, st, _ := do(t, "GET", "/api/share/audience", "", true); len(st["audience"].(map[string]any)["user_ids"].([]any)) != 0 || st["audience"].(map[string]any)["project"] != false {
		t.Errorf("the modal would show %v", st["audience"])
	}
	do(t, "PUT", "/api/sessions/c2/share", "", true)
	if len(p.apps) != 1 || p.apps[0].IsSharedWithTeam || len(p.apps[0].UserIDs) != 0 {
		t.Errorf("recreated as %+v", p.apps)
	}
}

// Stopping works even when the platform does not: Knowledge Worker Agent refuses guests
// from the moment shared_at is cleared.
func TestStopUnsharesWhateverThePlatformDoes(t *testing.T) {
	p := fakePlatform(t)
	newChat(t, "c1")
	do(t, "PUT", "/api/sessions/c1/share", "", true)
	p.failWith = errors.New("sidecar down")
	code, _, _ := do(t, "DELETE", "/api/sessions/c1/share", "", true)
	if code != 200 || db.IsSessionShared("c1") {
		t.Errorf("stop with the platform down: %d shared=%v", code, db.IsSessionShared("c1"))
	}
	if code, _, _ := do(t, "DELETE", "/api/sessions/c1/share", "", false); code != 200 || db.IsSessionShared("c1") {
		t.Errorf("stop without the token: %d", code)
	}
}

func TestDeletingTheLastSharedChatRetiresTheApp(t *testing.T) {
	p := fakePlatform(t)
	newChat(t, "c1")
	newChat(t, "plain")
	do(t, "PUT", "/api/sessions/c1/share", "", true)
	do(t, "DELETE", "/api/sessions/plain", "", true)
	if len(p.apps) != 1 {
		t.Fatal("deleting an unshared chat retired the app")
	}
	do(t, "DELETE", "/api/sessions/c1", "", true)
	if len(p.apps) != 0 {
		t.Errorf("deleting the last shared chat left %+v", p.apps)
	}
}

// Only project members can be chosen, the owner is not offered, and an
// edit sends the whole app back with only the audience changed.
func TestAudience(t *testing.T) {
	p := fakePlatform(t)
	if code, _, _ := do(t, "GET", "/api/share/audience", "", false); code != 403 {
		t.Errorf("audience without the token = %d", code)
	}
	code, st, _ := do(t, "GET", "/api/share/audience", "", true)
	ms, _ := st["members"].([]any)
	if code != 200 || len(ms) != 2 || st["app"] != false {
		t.Fatalf("audience = %d %v", code, st)
	}
	for _, m := range ms {
		if strings.EqualFold(m.(map[string]any)["email"].(string), "owner@example.com") {
			t.Error("the owner is offered to themselves")
		}
	}
	if code, _, _ := do(t, "PUT", "/api/share/audience", `{"user_ids":[99]}`, true); code != 400 {
		t.Errorf("a non-member was accepted: %d", code)
	}

	// Without an app, the choice is remembered for when one is made.
	do(t, "PUT", "/api/share/audience", `{"user_ids":[2,2]}`, true)
	if a := db.RememberedAudience(); len(a.UserIDs) != 1 || a.UserIDs[0] != 2 || p.count("edit") != 0 {
		t.Errorf("remembered %+v, edits %d", a, p.count("edit"))
	}

	newChat(t, "c1")
	do(t, "PUT", "/api/sessions/c1/share", "", true)
	p.apps[0].HTTPSEnabled, p.apps[0].OverrideHost, p.apps[0].Name = true, true, "Renamed by hand"
	code, st, _ = do(t, "PUT", "/api/share/audience", `{"user_ids":[2,3],"project":false}`, true)
	e := p.lastEdit
	if code != 200 || len(e.UserIDs) != 2 || !e.HTTPSEnabled || !e.OverrideHost || e.Name != "Renamed by hand" || e.Port != 8766 {
		t.Errorf("edit sent %+v (status %d)", e, code)
	}
	if aud := st["audience"].(map[string]any); len(aud["user_ids"].([]any)) != 2 || st["app"] != true {
		t.Errorf("audience after edit %v", st)
	}
}

// The link, presence and participants the modal shows.
func TestStateShowsWhoIsHere(t *testing.T) {
	p := fakePlatform(t)
	newChat(t, "c1")
	do(t, "PUT", "/api/sessions/c1/share", "", true)
	db.RecordGuest("c1", "g-1", "Sarah")
	db.RecordGuest("c1", "g-2", "Tom")
	v := chat.AddViewer("c1", "g-1")
	defer chat.RemoveViewer("c1", v)

	_, st, _ := do(t, "GET", "/api/sessions/c1/share", "", true)
	parts := st["participants"].([]any)
	present := map[string]bool{}
	for _, x := range parts {
		m := x.(map[string]any)
		present[m["name"].(string)] = m["present"].(bool)
	}
	if st["present"] != float64(1) || !present["Sarah"] || present["Tom"] {
		t.Errorf("state %v", st)
	}
	// Without the token the link cannot be looked up, and the modal says why.
	_, st, _ = do(t, "GET", "/api/sessions/c1/share", "", false)
	if st["share_url"] != "" || st["problem"] == nil || len(st["participants"].([]any)) != 2 {
		t.Errorf("state without token %v", st)
	}
	// An app deleted from under us is reported, not linked.
	p.apps = nil
	_, st, _ = do(t, "GET", "/api/sessions/c1/share", "", true)
	if st["share_url"] != "" || !strings.Contains(st["problem"].(string), "no longer exists") {
		t.Errorf("state with the app gone %v", st)
	}
}

// The sidebar learns about shared chats the owner does not have open.
func TestActivityCoversSharedChatsOnly(t *testing.T) {
	fakePlatform(t)
	newChat(t, "c1")
	newChat(t, "plain")
	do(t, "PUT", "/api/sessions/c1/share", "", true)
	v := chat.AddViewer("c1", "g-1")
	defer chat.RemoveViewer("c1", v)
	s := chat.OpenTurn("c1")
	s.Append(map[string]any{"type": "permission", "permission": "bash"})
	chat.OpenTurn("plain")

	_, body, _ := do(t, "GET", "/api/share/activity", "", false)
	rows := body["sessions"].([]any)
	if len(rows) != 1 {
		t.Fatalf("activity rows %v", rows)
	}
	r := rows[0].(map[string]any)
	if r["session_id"] != "c1" || r["present"] != float64(1) || r["streaming"] != true || r["waiting"] != true {
		t.Errorf("activity %v", r)
	}
	s.Append(map[string]any{"type": "tool_done"})
	_, body, _ = do(t, "GET", "/api/share/activity", "", false)
	if body["sessions"].([]any)[0].(map[string]any)["waiting"] != false {
		t.Error("still waiting after the turn moved on")
	}
}

// The token is the owner's platform credential; it is never returned or
// logged, on success or failure.
func TestTheTokenIsNeverEchoedOrLogged(t *testing.T) {
	p := fakePlatform(t)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	newChat(t, "c1")
	var bodies []string
	for _, fail := range []error{nil, &workspaceapps.Error{Status: 401, Code: 16, Message: "blocked"}} {
		p.failWith = fail
		for _, c := range [][2]string{{"PUT", "/api/sessions/c1/share"}, {"GET", "/api/sessions/c1/share"}, {"GET", "/api/share/audience"}, {"PUT", "/api/share/audience"}, {"DELETE", "/api/sessions/c1/share"}} {
			_, _, b := do(t, c[0], c[1], `{"user_ids":[]}`, true)
			bodies = append(bodies, b)
		}
	}
	for _, b := range bodies {
		if strings.Contains(b, token) {
			t.Fatalf("a response carried the token: %s", b)
		}
	}
	if strings.Contains(logs.String(), token) {
		t.Error("the token reached the log")
	}
}

// Two tabs sharing at once must not both create an app: the platform allows
// one per port, and the second create would fail the owner's action.
func TestConcurrentStartsCreateOneApp(t *testing.T) {
	p := fakePlatform(t)
	slowList := listApps
	listApps = func(ctx context.Context, tok string) ([]workspaceapps.App, error) {
		apps, err := slowList(ctx, tok)
		time.Sleep(20 * time.Millisecond) // widen the window between list and create
		return apps, err
	}
	var wg sync.WaitGroup
	for i := range 5 {
		id := "c" + string(rune('a'+i))
		newChat(t, id)
		wg.Add(1)
		go func() { defer wg.Done(); do(t, "PUT", "/api/sessions/"+id+"/share", "", true) }()
	}
	wg.Wait()
	if n := p.count("create"); n != 1 {
		t.Errorf("%d apps created for five concurrent shares", n)
	}
	if n := len(db.SharedSessionIDs()); n != 5 {
		t.Errorf("%d of 5 chats shared", n)
	}
}

// The toggles belong to a share. They change only on a shared
// chat, need no platform call, reach open guest pages, and are gone after a stop.
func TestShareOptionsBelongToTheShare(t *testing.T) {
	p := fakePlatform(t)
	base := t.TempDir()
	t.Setenv("HOME", filepath.Join(base, "home"))
	dir := filepath.Join(base, "home", "Chats", "c1")
	os.MkdirAll(dir, 0o755)
	if err := db.CreateSession("c1", config.SessionConfig{Workdir: dir}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { chat.Streams.Delete("c1") })

	if code, _, _ := do(t, "PUT", "/api/sessions/c1/share", `{"allow_files":true}`, true); code != 409 {
		t.Errorf("toggle on an unshared chat = %d, want 409", code)
	}
	do(t, "PUT", "/api/sessions/c1/share", "", true)
	calls := len(p.calls)

	guestView := chat.AddViewer("c1", "g-1")
	defer chat.RemoveViewer("c1", guestView)
	code, st, _ := do(t, "PUT", "/api/sessions/c1/share", `{"allow_files":true}`, false)
	if code != 200 || st["allow_files"] != true || st["allow_permissions"] != false || st["files_folder"] != dir {
		t.Errorf("files on without a token = %d %v", code, st)
	}
	select {
	case <-guestView.Options():
	default:
		t.Error("an open guest page was not told")
	}
	if _, st, _ := do(t, "PUT", "/api/sessions/c1/share", `{"allow_permissions":true}`, true); st["allow_files"] != true || st["allow_permissions"] != true {
		t.Errorf("one toggle reset the other: %v", st)
	}
	for _, c := range p.calls[calls:] {
		if c != "list" { // listing only builds the link shown with the state
			t.Errorf("a toggle changed the platform: %v", p.calls[calls:])
		}
	}
	if code, _, _ := do(t, "PUT", "/api/sessions/c1/share", `{"allow_files":"yes"}`, true); code != 400 {
		t.Errorf("a malformed toggle = %d", code)
	}

	do(t, "DELETE", "/api/sessions/c1/share", "", true)
	if o := db.SessionShareOptions("c1"); o.AllowFiles || o.AllowPermissions {
		t.Errorf("options outlived the share: %+v", o)
	}
	do(t, "PUT", "/api/sessions/c1/share", "", true)
	if _, st, _ := do(t, "GET", "/api/sessions/c1/share", "", true); st["allow_files"] != false || st["allow_permissions"] != false {
		t.Errorf("a new share started with %v", st)
	}
}

// A chat pointed at the home folder cannot share files at all.
func TestFilesAreRefusedForTheHomeFolder(t *testing.T) {
	fakePlatform(t)
	home := filepath.Join(t.TempDir(), "home")
	os.MkdirAll(home, 0o755)
	t.Setenv("HOME", home)
	if err := db.CreateSession("c1", config.SessionConfig{Workdir: home}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { chat.Streams.Delete("c1") })
	do(t, "PUT", "/api/sessions/c1/share", "", true)
	_, st, _ := do(t, "GET", "/api/sessions/c1/share", "", true)
	if why, _ := st["files_unavailable"].(string); !strings.Contains(why, "home folder") || st["files_folder"] != nil {
		t.Errorf("state = %v", st)
	}
	code, body, _ := do(t, "PUT", "/api/sessions/c1/share", `{"allow_files":true,"allow_permissions":true}`, true)
	if code != 409 || !strings.Contains(body["error"].(string), "home folder") {
		t.Errorf("files on for the home folder = %d %v", code, body)
	}
	if o := db.SessionShareOptions("c1"); o.AllowFiles || o.AllowPermissions {
		t.Errorf("a refused change was half-applied: %+v", o)
	}
}
