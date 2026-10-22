// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package fileshare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/fileshare/check"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/fileshare/viewer"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workspaceapps"
)

const token = "tok-SECRET-owner"

// platform stands in for the sidecar: one workspace's apps and members.
type platform struct {
	mu        sync.Mutex
	apps      []workspaceapps.App
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

func (p *platform) appOn(port int) (workspaceapps.App, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.apps {
		if int(a.Port) == port {
			return a, true
		}
	}
	return workspaceapps.App{}, false
}

// freeRange finds n consecutive ports free on this machine and makes them
// the range.
func freeRange(t *testing.T, n int) []int {
	t.Helper()
	for try := 0; try < 50; try++ {
		base := 40000 + rand.Intn(20000)
		ok := true
		for p := base; p < base+n && ok; p++ {
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
			if err != nil {
				ok = false
				break
			}
			ln.Close()
		}
		if ok {
			t.Setenv("KWA_ARTIFACT_PORTS", fmt.Sprintf("%d-%d", base, base+n-1))
			out := make([]int, n)
			for i := range out {
				out[i] = base + i
			}
			return out
		}
	}
	t.Fatal("no free ports")
	return nil
}

type rig struct {
	p      *platform
	ports  []int
	chat   string
	folder string
}

func setup(t *testing.T) *rig {
	t.Helper()
	if err := db.Init(filepath.Join(t.TempDir(), "t.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, a := range db.SharedArtifacts() {
			viewer.Unbind(a.Port)
		}
		db.Close()
	})
	r := &rig{p: &platform{nextID: 100}, ports: freeRange(t, 4), chat: "chat-1", folder: t.TempDir()}
	cfg := config.DefaultSessionConfig()
	cfg.Workdir, cfg.Label = r.folder, "Q3 numbers"
	if err := db.CreateSession(r.chat, cfg); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.folder, "report.html"), "<h1>Q3</h1>")
	p := r.p
	prev := []any{listApps, createApp, editApp, deleteApp, listMembers, config.OwnerEmail}
	t.Cleanup(func() {
		listApps = prev[0].(func(context.Context, string) ([]workspaceapps.App, error))
		createApp = prev[1].(func(context.Context, string, uint32, string, bool, []uint64) (workspaceapps.App, error))
		editApp = prev[2].(func(context.Context, string, workspaceapps.App) error)
		deleteApp = prev[3].(func(context.Context, string, uint64) error)
		listMembers = prev[4].(func(context.Context, string) ([]workspaceapps.Member, error))
		config.OwnerEmail = prev[5].(string)
	})
	config.OwnerEmail = "Owner@Example.com"
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
		for _, a := range p.apps {
			if a.Port == port {
				return workspaceapps.App{}, &workspaceapps.Error{Status: 400, Code: 3, Message: "the workspace application on this port is already created"}
			}
		}
		p.nextID++
		url := fmt.Sprintf("https://ws-7-port-%d.proxy.example", port)
		p.apps = append(p.apps, workspaceapps.App{ID: p.nextID, Port: port, Name: name, URL: url, Online: true, UserIDs: ids, IsSharedWithTeam: project})
		return workspaceapps.App{ID: p.nextID, Port: port, URL: url, Online: true}, nil
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
		return []workspaceapps.Member{{ID: 1, Email: "owner@example.com"}, {ID: 2, Email: "sarah@example.com"}, {ID: 3, Email: "tom@example.com"}}, nil
	}
	return r
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func call(t *testing.T, h http.HandlerFunc, method, path, id string, body any, withToken bool) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	if id != "" {
		req.SetPathValue("id", id)
	}
	if withToken {
		req.Header.Set("cloud_editor_user_token", token)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func (r *rig) share(t *testing.T, rel string, aud db.Audience) (Artifact, *httptest.ResponseRecorder) {
	t.Helper()
	rec := call(t, HandleShare, "POST", "/api/artifacts", "", shareRequest{SessionID: r.chat, Path: filepath.Join(r.folder, rel), Audience: aud}, true)
	var a Artifact
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	return a, rec
}

func fetch(t *testing.T, port int, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestShareCreatesAnAppOnTheFirstFreePort(t *testing.T) {
	r := setup(t)
	a, rec := r.share(t, "report.html", db.Audience{UserIDs: []uint64{2, 2}})
	if rec.Code != http.StatusOK {
		t.Fatalf("share: %d %s", rec.Code, rec.Body)
	}
	app, ok := r.p.appOn(r.ports[0])
	if !ok || app.Name != "Artifact: report.html" || app.IsSharedWithTeam || len(app.UserIDs) != 1 || app.UserIDs[0] != 2 {
		t.Fatalf("app: %+v (%v)", app, ok)
	}
	if a.Status != "shared" || a.Port != r.ports[0] || a.ChatLabel != "Q3 numbers" || a.Audience == nil || len(a.Audience.UserIDs) != 1 ||
		a.URL != fmt.Sprintf("https://ws-7-port-%d.proxy.example/", r.ports[0]) || a.AllowDownload {
		t.Errorf("artifact: %+v", a)
	}
	if code, body := fetch(t, a.Port, "/content"); code != 200 || !strings.HasSuffix(body, "</script><h1>Q3</h1>") {
		t.Errorf("served: %d %q", code, body)
	}
	if r.p.badTokens != 0 {
		t.Errorf("%d platform calls without the owner's token", r.p.badTokens)
	}
}

func TestShareRefusesWhatCantBeShared(t *testing.T) {
	r := setup(t)
	write(t, filepath.Join(r.folder, ".system", "x.html"), "x")
	write(t, filepath.Join(r.folder, "notes.txt"), "x")
	outside := filepath.Join(t.TempDir(), "secret.html")
	write(t, outside, "x")
	tmp := config.DefaultSessionConfig()
	tmp.Workdir, tmp.Temporary = r.folder, true
	if err := db.CreateSession("chat-tmp", tmp); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		req   shareRequest
		token bool
		code  int
	}{
		"no token":     {shareRequest{SessionID: r.chat, Path: filepath.Join(r.folder, "report.html")}, false, 403},
		"unknown chat": {shareRequest{SessionID: "nope", Path: filepath.Join(r.folder, "report.html")}, true, 404},
		"temporary":    {shareRequest{SessionID: "chat-tmp", Path: filepath.Join(r.folder, "report.html")}, true, 409},
		"outside":      {shareRequest{SessionID: r.chat, Path: outside}, true, 400},
		"relative":     {shareRequest{SessionID: r.chat, Path: "report.html"}, true, 400},
		"hidden":       {shareRequest{SessionID: r.chat, Path: filepath.Join(r.folder, ".system", "x.html")}, true, 409},
		"type":         {shareRequest{SessionID: r.chat, Path: filepath.Join(r.folder, "notes.txt")}, true, 409},
		"missing":      {shareRequest{SessionID: r.chat, Path: filepath.Join(r.folder, "gone.html")}, true, 409},
		"non-member":   {shareRequest{SessionID: r.chat, Path: filepath.Join(r.folder, "report.html"), Audience: db.Audience{UserIDs: []uint64{99}}}, true, 400},
	} {
		if rec := call(t, HandleShare, "POST", "/api/artifacts", "", c.req, c.token); rec.Code != c.code {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body, c.code)
		}
	}
	if len(db.SharedArtifacts()) != 0 || r.p.count("create") != 0 {
		t.Fatalf("something was shared: %+v", db.SharedArtifacts())
	}
	if _, rec := r.share(t, "report.html", db.Audience{}); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if _, rec := r.share(t, "report.html", db.Audience{}); rec.Code != http.StatusConflict {
		t.Errorf("sharing twice: %d", rec.Code)
	}
}

func TestShareSkipsTakenPortsAndStopsAtTheLimit(t *testing.T) {
	r := setup(t)
	// Someone's own app on the first port, and another program on the second.
	r.p.apps = append(r.p.apps, workspaceapps.App{ID: 1, Port: uint32(r.ports[0]), Name: "my dev server"})
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", r.ports[1]))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	for i, want := range []int{r.ports[2], r.ports[3]} {
		rel := fmt.Sprintf("r%d.html", i)
		write(t, filepath.Join(r.folder, rel), "x")
		if a, rec := r.share(t, rel, db.Audience{}); rec.Code != 200 || a.Port != want {
			t.Fatalf("share %d: port %d, want %d (%s)", i, a.Port, want, rec.Body)
		}
	}
	write(t, filepath.Join(r.folder, "r9.html"), "x")
	if _, rec := r.share(t, "r9.html", db.Audience{}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "All 4 places for shared files are taken") {
		t.Errorf("over the limit: %d %s", rec.Code, rec.Body)
	}
}

func TestAPlatformFailureLeavesNothingShared(t *testing.T) {
	r := setup(t)
	r.p.failWith = &workspaceapps.Error{Status: 400, Code: 3, Message: "reserved port"}
	_, rec := r.share(t, "report.html", db.Audience{})
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "reserved port") {
		t.Fatalf("share: %d %s", rec.Code, rec.Body)
	}
	if len(db.SharedArtifacts()) != 0 || viewer.Bound(r.ports[0]) {
		t.Error("a failed share left a row or a port behind")
	}
}

func TestStopRefusesViewersFirstThenDeletesTheApp(t *testing.T) {
	r := setup(t)
	a, _ := r.share(t, "report.html", db.Audience{})
	if rec := call(t, HandleStop, "DELETE", "/api/artifacts/"+a.ID, a.ID, nil, true); rec.Code != 200 {
		t.Fatalf("stop: %d %s", rec.Code, rec.Body)
	}
	if _, ok := db.GetSharedArtifact(a.ID); ok || viewer.Bound(a.Port) {
		t.Error("still shared")
	}
	if code, _ := fetch(t, a.Port, "/content"); code != 0 {
		t.Errorf("the port still answers: %d", code)
	}
	if _, ok := r.p.appOn(a.Port); ok {
		t.Error("the app is still there")
	}
}

func TestAStopWithoutTheTokenIsFinishedByTheNextList(t *testing.T) {
	r := setup(t)
	a, _ := r.share(t, "report.html", db.Audience{})
	// The user's own app on a port in the range, and one of ours elsewhere.
	r.p.apps = append(r.p.apps, workspaceapps.App{ID: 7, Port: uint32(r.ports[1]), Name: "my dev server"},
		workspaceapps.App{ID: 8, Port: 9, Name: "Artifact: outside the range"})
	if rec := call(t, HandleStop, "DELETE", "/", a.ID, nil, false); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if viewer.Bound(a.Port) {
		t.Error("viewers must be refused at once")
	}
	if _, ok := r.p.appOn(a.Port); !ok {
		t.Fatal("without the token the app can't be deleted yet")
	}
	call(t, HandleList, "GET", "/api/artifacts", "", nil, true)
	if _, ok := r.p.appOn(a.Port); ok {
		t.Error("the leftover app wasn't deleted")
	}
	if _, ok := r.p.appOn(r.ports[1]); !ok {
		t.Error("an app that isn't ours was deleted")
	}
	if _, ok := r.p.appOn(9); !ok {
		t.Error("an app outside the range was deleted")
	}
}

func TestChangingWhoCanOpenItSendsTheAppWhole(t *testing.T) {
	r := setup(t)
	a, _ := r.share(t, "report.html", db.Audience{})
	r.p.mu.Lock()
	for i := range r.p.apps {
		r.p.apps[i].OverrideHostValue = "keep-me"
	}
	r.p.mu.Unlock()
	rec := call(t, HandleAudience, "PUT", "/", a.ID, db.Audience{UserIDs: []uint64{3}, Project: true}, true)
	if rec.Code != 200 {
		t.Fatalf("audience: %d %s", rec.Code, rec.Body)
	}
	e := r.p.lastEdit
	if e.Name != "Artifact: report.html" || e.OverrideHostValue != "keep-me" || !e.IsSharedWithTeam || len(e.UserIDs) != 1 || e.UserIDs[0] != 3 {
		t.Errorf("edit: %+v", e)
	}
	var got Artifact
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Audience == nil || !got.Audience.Project {
		t.Errorf("reply: %s", rec.Body)
	}
	if rec := call(t, HandleAudience, "PUT", "/", a.ID, db.Audience{UserIDs: []uint64{1}}, true); rec.Code != http.StatusBadRequest {
		t.Errorf("the owner isn't offered: %d", rec.Code)
	}
}

func TestTheDownloadChoiceIsLocal(t *testing.T) {
	r := setup(t)
	a, _ := r.share(t, "report.html", db.Audience{})
	before := len(r.p.calls)
	if rec := call(t, HandleOptions, "PUT", "/", a.ID, map[string]bool{"allow_download": true}, false); rec.Code != 200 {
		t.Fatalf("options: %d %s", rec.Code, rec.Body)
	}
	if len(r.p.calls) != before {
		t.Error("the download choice called the platform")
	}
	if code, body := fetch(t, a.Port, "/download"); code != 200 || body != "<h1>Q3</h1>" {
		t.Errorf("download: %d %q", code, body)
	}
}

func TestListReportsWhatsWrong(t *testing.T) {
	r := setup(t)
	write(t, filepath.Join(r.folder, "b.html"), "b")
	write(t, filepath.Join(r.folder, "c.html"), "c")
	missing, _ := r.share(t, "report.html", db.Audience{})
	removed, _ := r.share(t, "b.html", db.Audience{})
	unavailable, _ := r.share(t, "c.html", db.Audience{})
	os.Remove(filepath.Join(r.folder, "report.html"))
	app, _ := r.p.appOn(removed.Port)
	_ = deleteApp(context.Background(), token, app.ID)
	viewer.Unbind(unavailable.Port)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", unavailable.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var resp listResponse
	_ = json.Unmarshal(call(t, HandleList, "GET", "/", "", nil, true).Body.Bytes(), &resp)
	got := map[string]string{}
	for _, a := range resp.Artifacts {
		got[a.ID] = a.Status
	}
	want := map[string]string{missing.ID: "missing", removed.ID: "removed", unavailable.ID: "unavailable"}
	for id, st := range want {
		if got[id] != st {
			t.Errorf("%s: %q, want %q", id, got[id], st)
		}
	}
	if !resp.Enabled || resp.Limit != 4 {
		t.Errorf("enabled=%v limit=%d", resp.Enabled, resp.Limit)
	}
	// Without the token nothing can be said about the apps.
	_ = json.Unmarshal(call(t, HandleList, "GET", "/", "", nil, false).Body.Bytes(), &resp)
	for _, a := range resp.Artifacts {
		if a.ID == removed.ID && (a.Status != "shared" || a.Audience != nil) {
			t.Errorf("without the token: %+v", a)
		}
	}
	if resp.Problem == "" {
		t.Error("the list must say the token is missing")
	}
}

func TestRepairRecreatesAnAppOrMovesToAFreePort(t *testing.T) {
	r := setup(t)
	write(t, filepath.Join(r.folder, "b.html"), "b")
	a, _ := r.share(t, "report.html", db.Audience{UserIDs: []uint64{2}})
	b, _ := r.share(t, "b.html", db.Audience{UserIDs: []uint64{3}})

	app, _ := r.p.appOn(a.Port)
	_ = deleteApp(context.Background(), token, app.ID)
	if rec := call(t, HandleRepair, "POST", "/", a.ID, nil, true); rec.Code != 200 {
		t.Fatalf("repair removed: %d %s", rec.Code, rec.Body)
	}
	again, ok := r.p.appOn(a.Port)
	if !ok || len(again.UserIDs) != 0 || again.IsSharedWithTeam {
		t.Errorf("a recreated app keeps the link and is shared with no one: %+v", again)
	}

	viewer.Unbind(b.Port)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", b.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	rec := call(t, HandleRepair, "POST", "/", b.ID, nil, true)
	var moved Artifact
	_ = json.Unmarshal(rec.Body.Bytes(), &moved)
	if rec.Code != 200 || moved.Port == b.Port || moved.Status != "shared" {
		t.Fatalf("repair unavailable: %d %s", rec.Code, rec.Body)
	}
	if app, ok := r.p.appOn(moved.Port); !ok || len(app.UserIDs) != 1 || app.UserIDs[0] != 3 {
		t.Errorf("the moved app keeps who can open it: %+v", app)
	}
	if _, ok := r.p.appOn(b.Port); ok {
		t.Error("the old app is still there")
	}
	if code, body := fetch(t, moved.Port, "/content"); code != 200 || !strings.HasSuffix(body, "</script>b") {
		t.Errorf("served after the move: %d %q", code, body)
	}
}

func TestDeletingAChatStopsSharingOnlyWhenItsFolderGoes(t *testing.T) {
	r := setup(t)
	a, _ := r.share(t, "report.html", db.Audience{})
	keptFolder := t.TempDir()
	cfg := config.DefaultSessionConfig()
	cfg.Workdir = keptFolder
	if err := db.CreateSession("chat-kept", cfg); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(keptFolder, "kept.html"), "k")
	kept, rec := (&rig{p: r.p, chat: "chat-kept", folder: keptFolder}).share(t, "kept.html", db.Audience{})
	if rec.Code != 200 {
		t.Fatal(rec.Body)
	}

	deleteBoth := func(w http.ResponseWriter, _ *http.Request) {
		db.DeleteSession(r.chat)
		os.RemoveAll(r.folder)
		db.DeleteSession("chat-kept")
	}
	call(t, AfterChatsDeleted(deleteBoth), "DELETE", "/", "", nil, true)
	if _, ok := db.GetSharedArtifact(a.ID); ok || viewer.Bound(a.Port) {
		t.Error("the file whose folder went is still shared")
	}
	if _, ok := r.p.appOn(a.Port); ok {
		t.Error("its app is still there")
	}
	got, ok := db.GetSharedArtifact(kept.ID)
	if !ok || got.SessionID != "" || !viewer.Bound(kept.Port) {
		t.Errorf("the file whose folder stayed: %+v %v", got, ok)
	}
}

func TestStartServesEverySharedFile(t *testing.T) {
	r := setup(t)
	a, _ := r.share(t, "report.html", db.Audience{})
	viewer.Unbind(a.Port)
	Start()
	if code, _ := fetch(t, a.Port, "/content"); code != 200 {
		t.Errorf("after start: %d", code)
	}
}

func TestNothingIsServedWhileSharingIsOff(t *testing.T) {
	r := setup(t)
	a, _ := r.share(t, "report.html", db.Audience{})
	on := os.Getenv("KWA_ARTIFACT_PORTS")
	viewer.Unbind(a.Port)
	t.Setenv("KWA_ARTIFACT_PORTS", "off")
	Start()
	if code, _ := fetch(t, a.Port, "/content"); code != 0 {
		t.Errorf("served while sharing is off: %d", code)
	}
	var resp listResponse
	_ = json.Unmarshal(call(t, HandleList, "GET", "/", "", nil, true).Body.Bytes(), &resp)
	if resp.Enabled || len(resp.Artifacts) != 1 || resp.Artifacts[0].Status != "off" || resp.Artifacts[0].Problem == "" {
		t.Errorf("list while off: %+v", resp)
	}
	if rec := call(t, HandleRepair, "POST", "/", a.ID, nil, true); rec.Code != http.StatusConflict || viewer.Bound(a.Port) {
		t.Errorf("repair while off: %d %s", rec.Code, rec.Body)
	}
	if _, ok := r.p.appOn(a.Port); !ok {
		t.Error("turning sharing off must keep the file's app, for when it's back on")
	}

	t.Setenv("KWA_ARTIFACT_PORTS", on)
	Start()
	if code, _ := fetch(t, a.Port, "/content"); code != 200 {
		t.Errorf("served again once on: %d", code)
	}
}

func TestPorts(t *testing.T) {
	for v, want := range map[string]string{
		"": "8770-8799", "9000": "9000-9000", " 9000 - 9002 ": "9000-9002", "off": "none", "1110-1112": "1110-1112 without 1111",
	} {
		t.Setenv("KWA_ARTIFACT_PORTS", v)
		ps, err := ports()
		if err != nil {
			t.Errorf("%q: %v", v, err)
			continue
		}
		switch want {
		case "none":
			if len(ps) != 0 {
				t.Errorf("off: %v", ps)
			}
		case "1110-1112 without 1111":
			if fmt.Sprint(ps) != "[1110 1112]" {
				t.Errorf("reserved: %v", ps)
			}
		default:
			if got := fmt.Sprintf("%d-%d", ps[0], ps[len(ps)-1]); got != want {
				t.Errorf("%q: %s", v, got)
			}
		}
	}
	for _, bad := range []string{"x", "9002-9000", "0-5", "1-70000", "1000-3000"} {
		t.Setenv("KWA_ARTIFACT_PORTS", bad)
		if _, err := ports(); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestErrorsNeverUse503(t *testing.T) {
	rec := httptest.NewRecorder()
	writeFailure(rec, errors.New("boom"))
	if rec.Code == http.StatusServiceUnavailable {
		t.Error("503 bodies are emptied by the platform proxy")
	}
}

const cdnPage = `<html><script src="https://cdn.jsdelivr.net/npm/chart.js"></script></html>`

func TestAFileThatIsntSelfContainedIsntShared(t *testing.T) {
	r := setup(t)
	write(t, filepath.Join(r.folder, "report.html"), cdnPage)
	_, rec := r.share(t, "report.html", db.Audience{})
	var body struct {
		Error    string          `json:"error"`
		Findings []check.Finding `json:"findings"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusConflict || body.Error != notSelfContained || len(body.Findings) != 1 ||
		body.Findings[0].What != "Loads a script from cdn.jsdelivr.net" {
		t.Fatalf("share: %d %s", rec.Code, rec.Body)
	}
	if len(db.SharedArtifacts()) != 0 || r.p.count("create") != 0 || viewer.Bound(r.ports[0]) {
		t.Error("a refused file left something behind")
	}
}

func TestWarningsDontStopSharing(t *testing.T) {
	r := setup(t)
	write(t, filepath.Join(r.folder, "report.html"), `<p>hi</p><script>localStorage.x = 1</script>`)
	a, rec := r.share(t, "report.html", db.Audience{})
	if rec.Code != 200 || a.Status != "shared" || len(a.Findings) != 1 || a.Findings[0].Blocking {
		t.Fatalf("share: %d %s", rec.Code, rec.Body)
	}
}

func TestAMarkdownFileIsCheckedToo(t *testing.T) {
	r := setup(t)
	write(t, filepath.Join(r.folder, "notes.md"), "# Notes\n\n![chart](https://img.example/q3.png)\n")
	if _, rec := r.share(t, "notes.md", db.Audience{}); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "Shows a picture from img.example") {
		t.Fatalf("share: %d %s", rec.Code, rec.Body)
	}
	write(t, filepath.Join(r.folder, "notes.md"), "# Notes\n\nSee <b>this</b>.\n")
	a, rec := r.share(t, "notes.md", db.Audience{})
	if rec.Code != 200 || a.Status != "shared" || len(a.Findings) != 1 || a.Findings[0].What != "Has HTML written into it, which isn't shown" {
		t.Fatalf("share: %d %s", rec.Code, rec.Body)
	}
}

func TestTheCheckRoute(t *testing.T) {
	r := setup(t)
	write(t, filepath.Join(r.folder, "cdn.html"), cdnPage)
	for name, want := range map[string]bool{"report.html": true, "cdn.html": false} {
		rec := call(t, HandleCheck, "POST", "/api/artifacts/check", "", fileRequest{SessionID: r.chat, Path: filepath.Join(r.folder, name)}, false)
		var got struct {
			SelfContained bool            `json:"self_contained"`
			Findings      []check.Finding `json:"findings"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if rec.Code != 200 || got.SelfContained != want || got.Findings == nil {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := call(t, HandleCheck, "POST", "/", "", fileRequest{SessionID: r.chat, Path: filepath.Join(r.folder, "notes.txt")}, false); rec.Code != http.StatusConflict {
		t.Errorf("a file that can't be shared: %d", rec.Code)
	}
}

func TestASharedFileThatStopsPassingNeedsAttention(t *testing.T) {
	r := setup(t)
	a, _ := r.share(t, "report.html", db.Audience{})
	list := func() Artifact {
		var resp listResponse
		_ = json.Unmarshal(call(t, HandleList, "GET", "/", "", nil, true).Body.Bytes(), &resp)
		return resp.Artifacts[0]
	}
	path := filepath.Join(r.folder, "report.html")
	write(t, path, cdnPage)
	if got := list(); got.Status != "attention" || len(got.Findings) != 1 || got.Problem == "" {
		t.Fatalf("after the change: %+v", got)
	}
	stored, _ := db.GetSharedArtifact(a.ID)
	if info, _ := os.Stat(path); stored.CheckKey != checkKey(info) || !strings.Contains(stored.CheckJSON, "cdn.jsdelivr.net") {
		t.Errorf("the check wasn't kept: %+v", stored)
	}
	// A cached result is used as long as the file is unchanged.
	if err := db.SetArtifactCheck(a.ID, stored.CheckKey, `{"findings":[],"blocking":false}`); err != nil {
		t.Fatal(err)
	}
	if got := list(); got.Status != "shared" {
		t.Errorf("the cache wasn't used: %+v", got)
	}
	write(t, path, "<p>fixed</p>")
	if got := list(); got.Status != "shared" || len(got.Findings) != 0 {
		t.Errorf("after the fix: %+v", got)
	}
}

func sharedAt(t *testing.T, id string) string {
	t.Helper()
	a, ok := db.GetSharedArtifact(id)
	if !ok {
		return ""
	}
	return a.RelPath
}

func TestASharedFileFollowsARenameOrAMove(t *testing.T) {
	r := setup(t)
	write(t, filepath.Join(r.folder, "sub", "chart.html"), "<p>chart</p>")
	write(t, filepath.Join(r.folder, "report.html.bak"), "old")
	a, _ := r.share(t, "report.html", db.Audience{})
	b, _ := r.share(t, "sub/chart.html", db.Audience{})
	move := func(src, dest string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(r.folder, dest)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(r.folder, src), filepath.Join(r.folder, dest)); err != nil {
			t.Fatal(err)
		}
		Moved(filepath.Join(r.folder, src), filepath.Join(r.folder, dest))
	}
	move("report.html.bak", "old.html")
	move("report.html", "q3.html")
	move("sub", "charts")
	move("charts/chart.html", "charts/2026/chart.html")
	if got := sharedAt(t, a.ID) + " " + sharedAt(t, b.ID); got != "q3.html charts/2026/chart.html" {
		t.Fatalf("paths: %s", got)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/content", a.Port))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.HasSuffix(string(body), "</script><h1>Q3</h1>") {
		t.Errorf("the link serves %q", body)
	}
	rec := call(t, HandleList, "GET", "/api/artifacts", "", nil, true)
	if strings.Count(rec.Body.String(), `"status":"shared"`) != 2 {
		t.Errorf("list: %s", rec.Body)
	}
}

func TestAFileMovedOutOfItsChatsFolderShowsMissing(t *testing.T) {
	r := setup(t)
	a, _ := r.share(t, "report.html", db.Audience{})
	elsewhere := filepath.Join(t.TempDir(), "report.html")
	if err := os.Rename(filepath.Join(r.folder, "report.html"), elsewhere); err != nil {
		t.Fatal(err)
	}
	Moved(filepath.Join(r.folder, "report.html"), elsewhere)
	if sharedAt(t, a.ID) != "report.html" {
		t.Fatalf("path: %q", sharedAt(t, a.ID))
	}
	rec := call(t, HandleList, "GET", "/api/artifacts", "", nil, true)
	if !strings.Contains(rec.Body.String(), `"status":"missing"`) {
		t.Errorf("list: %s", rec.Body)
	}
}

func TestDeletingStopsSharingWhatGoes(t *testing.T) {
	r := setup(t)
	write(t, filepath.Join(r.folder, "sub", "chart.html"), "<p>chart</p>")
	write(t, filepath.Join(r.folder, "subway.html"), "<p>next door</p>")
	a, _ := r.share(t, "report.html", db.Audience{})
	b, _ := r.share(t, "sub/chart.html", db.Audience{})
	c, _ := r.share(t, "subway.html", db.Audience{})
	req := httptest.NewRequest("POST", "/api/files/delete", nil)
	req.Header.Set("cloud_editor_user_token", token)
	Deleting(req, filepath.Join(r.folder, "sub"))
	if sharedAt(t, b.ID) != "" || viewer.Bound(b.Port) {
		t.Error("the file in the deleted folder is still shared")
	}
	if _, has := r.p.appOn(b.Port); has {
		t.Error("its app is still there")
	}
	if sharedAt(t, a.ID) == "" || sharedAt(t, c.ID) == "" {
		t.Error("a file outside the deleted folder stopped being shared")
	}
	Deleting(req, filepath.Join(r.folder, "report.html"))
	if sharedAt(t, a.ID) != "" || sharedAt(t, c.ID) == "" || r.p.count("delete") != 2 || r.p.badTokens != 0 {
		t.Errorf("after deleting report.html: %q %q, %d deletes", sharedAt(t, a.ID), sharedAt(t, c.ID), r.p.count("delete"))
	}
}

func TestARenamedFilesAppTakesItsName(t *testing.T) {
	r := setup(t)
	a, _ := r.share(t, "report.html", db.Audience{UserIDs: []uint64{2}})
	if err := os.Rename(filepath.Join(r.folder, "report.html"), filepath.Join(r.folder, "q3.html")); err != nil {
		t.Fatal(err)
	}
	Moved(filepath.Join(r.folder, "report.html"), filepath.Join(r.folder, "q3.html"))
	call(t, HandleList, "GET", "/api/artifacts", "", nil, false)
	if app, _ := r.p.appOn(a.Port); app.Name != "Artifact: report.html" || r.p.count("edit") != 0 {
		t.Fatalf("without the token nothing is asked of the platform: %q, %d edits", app.Name, r.p.count("edit"))
	}
	rec := call(t, HandleList, "GET", "/api/artifacts", "", nil, true)
	app, _ := r.p.appOn(a.Port)
	if app.Name != "Artifact: q3.html" || len(app.UserIDs) != 1 || app.UserIDs[0] != 2 || r.p.count("edit") != 1 {
		t.Fatalf("app after the list: %+v, %d edits", app, r.p.count("edit"))
	}
	if !strings.Contains(rec.Body.String(), `"name":"q3.html"`) || !strings.Contains(rec.Body.String(), `"status":"shared"`) {
		t.Errorf("list: %s", rec.Body)
	}
	call(t, HandleList, "GET", "/api/artifacts", "", nil, true)
	if r.p.count("edit") != 1 {
		t.Errorf("an app already named for its file is left alone: %d edits", r.p.count("edit"))
	}
}
