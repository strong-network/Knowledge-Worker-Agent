// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package guest

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

const testOrigin = "https://ws-7-port-8766.proxy.example"

var testFiles = fstest.MapFS{
	"index.html":           {Data: []byte("<!doctype html><title>guest</title>")},
	"assets/app.js":        {Data: []byte("console.log('app')")},
	"sw.js":                {Data: []byte("self.skipWaiting()")},
	"manifest.webmanifest": {Data: []byte("{}")},
}

type guestEnv struct {
	t      *testing.T
	srv    *httptest.Server
	c      *http.Client
	shared string
	hidden string
}

func newEnv(t *testing.T) *guestEnv {
	t.Helper()
	if err := db.Init(filepath.Join(t.TempDir(), "t.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &guestEnv{t: t, shared: chat.NewUUID(), hidden: chat.NewUUID()}
	for _, sid := range []string{e.shared, e.hidden} {
		if err := db.CreateSession(sid, config.SessionConfig{Label: "Launch plan"}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { chat.Streams.Delete(sid); chat.ClearQueue(sid) })
	}
	db.SetSessionShared(e.shared, true)
	s, err := newServer(testFiles, testOrigin, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	e.srv = httptest.NewServer(s.routes())
	t.Cleanup(func() { e.srv.CloseClientConnections(); e.srv.Close() })
	jar, _ := cookiejar.New(nil)
	e.c = e.srv.Client()
	e.c.Jar = jar
	e.c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return e
}

func (e *guestEnv) do(method, path, body string, hdr map[string]string) (int, string, http.Header) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

var sameOrigin = map[string]string{"Origin": testOrigin, "Content-Type": "application/json"}

func (e *guestEnv) identify(name string) {
	e.t.Helper()
	if code, body, _ := e.do("POST", "/guest/identify", `{"name":`+strconv.Quote(name)+`}`, sameOrigin); code != 200 {
		e.t.Fatalf("identify: %d %s", code, body)
	}
}

// Only what is registered here exists. Every owner route, and every file of
// the embedded tree other than the page and its assets, is simply absent.
func TestNothingButTheGuestRoutesExists(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{
		"/", "/index.html", "/sw.js", "/manifest.webmanifest",
		"/api/sessions", "/api/history?session_id=" + e.shared,
		"/api/files/view?path=/etc/passwd", "/api/browse?path=/",
		"/api/mcp/servers", "/api/providers", "/api/share/platform",
		"/api/sessions/" + e.shared + "/events", "/api/sessions/" + e.shared + "/share",
		"/assets/../index.html", "/assets/", "/assets/nope.js",
	} {
		code, body, _ := e.do("GET", p, "", nil)
		if code == 200 || strings.Contains(body, "<title>guest</title>") && p != "/s/"+e.shared {
			t.Errorf("GET %s = %d, want it absent", p, code)
		}
	}
	for _, p := range []string{"/api/chat", "/api/sessions/new", "/api/mcp/servers", "/api/voice/dictations", "/guest/api/sessions/" + e.shared + "/answer"} {
		if code, _, _ := e.do("POST", p, "{}", sameOrigin); code != 404 && code != 405 {
			t.Errorf("POST %s = %d, want it absent", p, code)
		}
	}
	if code, body, _ := e.do("GET", "/assets/app.js", "", nil); code != 200 || !strings.Contains(body, "console.log") {
		t.Errorf("the page's own asset is not served: %d", code)
	}
}

// An unshared chat is indistinguishable from one that does not exist.
func TestUnsharedChatLooksExactlyLikeAMissingOne(t *testing.T) {
	e := newEnv(t)
	e.identify("Sarah")
	for _, tmpl := range []string{"/s/%s", "/guest/api/sessions/%s", "/guest/api/sessions/%s/history", "/guest/api/sessions/%s/events"} {
		hc, hb, _ := e.do("GET", strings.Replace(tmpl, "%s", e.hidden, 1), "", nil)
		mc, mb, _ := e.do("GET", strings.Replace(tmpl, "%s", "no-such-id", 1), "", nil)
		if hc != 404 || hc != mc || hb != mb {
			t.Errorf("%s: unshared=%d %q, missing=%d %q", tmpl, hc, hb, mc, mb)
		}
	}
	code, _, _ := e.do("POST", "/guest/api/chat", `{"session_id":"`+e.hidden+`","prompt":"hi"}`, sameOrigin)
	if code != 404 {
		t.Errorf("chat on an unshared session = %d", code)
	}
}

func TestSharedPageIsServedWithProtectiveHeaders(t *testing.T) {
	e := newEnv(t)
	code, body, h := e.do("GET", "/s/"+e.shared, "", nil)
	if code != 200 || !strings.Contains(body, "<title>guest</title>") {
		t.Fatalf("page: %d", code)
	}
	for k, want := range map[string]string{
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "frame-ancestors 'none'",
	} {
		if h.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, h.Get(k), want)
		}
	}
}

// SameSite is no CSRF protection between Workspace Apps, so every POST
// must come from the share URL's own origin — and none at all without one.
func TestPostsRequireTheShareOrigin(t *testing.T) {
	e := newEnv(t)
	for _, origin := range []string{"", "https://evil.example", "https://ws-7-port-8767.proxy.example", "http://localhost"} {
		hdr := map[string]string{"Content-Type": "application/json"}
		if origin != "" {
			hdr["Origin"] = origin
		}
		if code, _, _ := e.do("POST", "/guest/identify", `{"name":"Mallory"}`, hdr); code != 403 {
			t.Errorf("identify from origin %q = %d, want 403", origin, code)
		}
		if code, _, _ := e.do("POST", "/guest/api/chat", `{"session_id":"`+e.shared+`","prompt":"hi"}`, hdr); code != 403 {
			t.Errorf("chat from origin %q = %d, want 403", origin, code)
		}
	}

	s, _ := newServer(testFiles, "", nil)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/guest/identify", strings.NewReader(`{"name":"x"}`))
	r.Header.Set("Origin", "")
	s.routes().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Errorf("with no expected origin a POST got %d, want 403", w.Code)
	}
}

func TestIdentityCookieIsHostOnlyAndProtected(t *testing.T) {
	e := newEnv(t)
	req, _ := http.NewRequest("POST", e.srv.URL+"/guest/identify", strings.NewReader(`{"name":"Sarah"}`))
	req.Header.Set("Origin", testOrigin)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	raw := resp.Header.Get("Set-Cookie")
	for _, want := range []string{"kwa_guest=", "Path=/", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(raw, want) {
			t.Errorf("Set-Cookie %q lacks %s", raw, want)
		}
	}
	if strings.Contains(strings.ToLower(raw), "domain=") {
		t.Errorf("cookie is not host-only: %q", raw)
	}
}

func TestIdentityIsReadFromTheCookieNamedBeforeTheRename(t *testing.T) {
	cookie := func(name, id string) *http.Cookie {
		raw, _ := json.Marshal(identity{ID: id, Name: "Sarah"})
		return &http.Cookie{Name: name, Value: base64.RawURLEncoding.EncodeToString(raw)}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(cookie("sds_guest", "g-old"))
	if id, ok := readIdentity(r); !ok || id.ID != "g-old" {
		t.Errorf("old cookie: got %+v, %v", id, ok)
	}
	r.AddCookie(cookie(cookieName, "g-new"))
	if id, ok := readIdentity(r); !ok || id.ID != "g-new" {
		t.Errorf("both cookies: got %+v, %v; want the current name to win", id, ok)
	}
}

func TestIdentifyCleansTheNameAndKeepsTheIDOnRename(t *testing.T) {
	e := newEnv(t)
	if code, _, _ := e.do("POST", "/guest/identify", `{"name":"  \u0000\u202e "}`, sameOrigin); code != 400 {
		t.Errorf("a name of only control characters was accepted: %d", code)
	}
	e.identify("Sa\u202erah \n\t  Jones")
	_, body, _ := e.do("GET", "/guest/api/identity", "", nil)
	var first struct{ ID, Name string }
	json.Unmarshal([]byte(body), &first)
	if first.Name != "Sarah Jones" || first.ID == "" {
		t.Fatalf("identity = %+v", first)
	}
	e.identify("Sarah J.")
	_, body, _ = e.do("GET", "/guest/api/identity", "", nil)
	var second struct{ ID, Name string }
	json.Unmarshal([]byte(body), &second)
	if second.ID != first.ID || second.Name != "Sarah J." {
		t.Errorf("rename changed identity: %+v -> %+v", first, second)
	}
	if n := cleanName(strings.Repeat("x", 200)); len([]rune(n)) != maxNameLen {
		t.Errorf("name not capped: %d runes", len([]rune(n)))
	}
}

func TestMetaIsOpenButHistoryNeedsAName(t *testing.T) {
	e := newEnv(t)
	code, body, _ := e.do("GET", "/guest/api/sessions/"+e.shared, "", nil)
	if code != 200 || !strings.Contains(body, `"title":"Launch plan"`) {
		t.Errorf("meta: %d %s", code, body)
	}
	if code, _, _ := e.do("GET", "/guest/api/sessions/"+e.shared+"/history", "", nil); code != 401 {
		t.Errorf("history without identity = %d, want 401", code)
	}
	db.AddUserMessage(e.shared, "hello", "", "", "")
	db.AddMessageWithUsage(e.shared, "assistant", "hi", `{"cost":9.99}`)
	e.identify("Sarah")
	code, body, _ = e.do("GET", "/guest/api/sessions/"+e.shared+"/history", "", nil)
	if code != 200 || strings.Contains(body, "9.99") || !strings.Contains(body, `"content":"hi"`) {
		t.Errorf("history: %d %s", code, body)
	}
}

// A guest's prompt is accepted without a stream, and carries the guest as
// its author into the queue.
func TestChatAcceptsWithoutStreamingAndAttributesTheGuest(t *testing.T) {
	e := newEnv(t)
	if code, _, _ := e.do("POST", "/guest/api/chat", `{"session_id":"`+e.shared+`","prompt":"hi"}`, sameOrigin); code != 401 {
		t.Errorf("chat without identity = %d, want 401", code)
	}
	e.identify("Sarah")
	chat.OpenTurn(e.shared) // a running turn, so the prompt queues instead of spawning opencode

	code, body, h := e.do("POST", "/guest/api/chat", `{"session_id":"`+e.shared+`","prompt":"what next?"}`, sameOrigin)
	if code != 202 || !strings.Contains(body, `"status":"queued"`) {
		t.Fatalf("chat: %d %s", code, body)
	}
	if ct := h.Get("Content-Type"); ct != "application/json" {
		t.Errorf("chat answered with %q; guests must not get a stream", ct)
	}
	q := chat.SnapshotQueue(e.shared)
	if len(q) != 1 || q[0].Prompt != "what next?" || q[0].AuthorName != "Sarah" || q[0].AuthorID == "" {
		t.Errorf("queued item = %+v", q)
	}
	if code, _, _ := e.do("POST", "/guest/api/chat", `{"session_id":"`+e.shared+`","prompt":"   "}`, sameOrigin); code != 400 {
		t.Errorf("empty prompt = %d", code)
	}
}

// End to end over HTTP: a guest on /events gets the turn filtered, and is told
// when sharing ends.
func TestGuestEventsAreFilteredAndEndWhenUnshared(t *testing.T) {
	e := newEnv(t)
	e.identify("Sarah")
	req, _ := http.NewRequest("GET", e.srv.URL+"/guest/api/sessions/"+e.shared+"/events", nil)
	resp, err := e.c.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("events: %v %v", err, resp.Status)
	}
	defer resp.Body.Close()
	lines := make(chan string, 256)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			// share_options comes from its own goroutine, so its place among
			// turn frames is not fixed; options_test.go covers it.
			if l := sc.Text(); strings.HasPrefix(l, "data: ") && !strings.Contains(l, `"type":"share_options"`) {
				lines <- l[len("data: "):]
			}
		}
	}()
	next := func() map[string]any {
		t.Helper()
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("stream closed")
			}
			var m map[string]any
			json.Unmarshal([]byte(l), &m)
			return m
		case <-time.After(3 * time.Second):
			t.Fatal("no frame")
		}
		return nil
	}
	if f := next(); f["type"] != "ready" {
		t.Fatalf("first frame %v", f)
	}

	s := chat.OpenTurnFor(e.shared, "deploy it", chat.Author{ID: "g-2", Name: "Omar"})
	s.Append(map[string]any{"type": "tool_call", "call_id": "c", "tool": "bash", "args": `{"command":"cat SECRET"}`})
	s.Append(map[string]any{"type": "usage", "cost": 3.14})
	s.Append(map[string]any{"type": "chunk", "text": "done"})
	s.Finish()

	if f := next(); f["type"] != "turn_start" || f["prompt"] != "deploy it" || f["author_name"] != "Omar" {
		t.Errorf("turn_start %v", f)
	}
	if f := next(); f["type"] != "tool_call" || f["args"] != nil {
		t.Errorf("tool_call reached the guest unfiltered: %v", f)
	}
	if f := next(); f["type"] != "chunk" {
		t.Errorf("usage was not dropped; next frame %v", f)
	}

	db.SetSessionShared(e.shared, false)
	chat.CloseViewers(e.shared, "sharing_ended", "")
	if f := next(); f["type"] != "sharing_ended" {
		t.Errorf("final frame %v, want sharing_ended", f)
	}
}

// Watching the chat makes someone a participant; holding its link, reading its
// history or knocking on an unshared chat does not.
func TestOpeningTheLiveViewRecordsTheParticipant(t *testing.T) {
	e := newEnv(t)
	e.identify("Sarah")
	e.do("GET", "/s/"+e.shared, "", nil)
	e.do("GET", "/guest/api/sessions/"+e.shared+"/history", "", nil)
	e.do("GET", "/guest/api/sessions/"+e.hidden+"/events", "", nil)
	if g, _ := db.ListGuests(e.shared); len(g) != 0 {
		t.Fatalf("recorded before the live view opened: %+v", g)
	}
	if g, _ := db.ListGuests(e.hidden); len(g) != 0 {
		t.Fatalf("recorded on an unshared chat: %+v", g)
	}

	req, _ := http.NewRequest("GET", e.srv.URL+"/guest/api/sessions/"+e.shared+"/events", nil)
	resp, err := e.c.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("events: %v %v", err, resp.Status)
	}
	bufio.NewReader(resp.Body).ReadString('\n') // the ready frame: the handler is past recording
	resp.Body.Close()
	_, body, _ := e.do("GET", "/guest/api/identity", "", nil)
	var me struct{ ID string }
	json.Unmarshal([]byte(body), &me)
	if g, _ := db.ListGuests(e.shared); len(g) != 1 || g[0].ID != me.ID || g[0].Name != "Sarah" {
		t.Errorf("participants = %+v, want Sarah (%s)", g, me.ID)
	}
}

// D21, transitively: nothing the guest package imports, however indirectly,
// may be the sidecar client or the owner's sharing routes. A direct-import
// check alone would miss guest -> sessions -> anything that calls the sidecar.
func TestGuestDependenciesExcludeTheSidecar(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) < 5 {
		t.Fatalf("go list returned %d packages; the check would pass vacuously", len(deps))
	}
	for _, d := range deps {
		if strings.HasSuffix(d, "/internal/workspaceapps") || strings.HasSuffix(d, "/internal/sharing") {
			t.Errorf("the guest listener depends on %s", d)
		}
	}
}

// The guest surface must never be able to reach the owner's platform
// token, which the proxy will deliver to any port. Enforced structurally.
func TestGuestPackageCannotReachTheOwnerToken(t *testing.T) {
	fset := token.NewFileSet()
	entries, _ := os.ReadDir(".")
	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if strings.Contains(imp.Path.Value, "internal/workspaceapps") {
				t.Errorf("%s imports the sidecar client", name)
			}
		}
		src, _ := os.ReadFile(name)
		if strings.Contains(strings.ToLower(string(src)), "cloud_editor_user_token") {
			t.Errorf("%s mentions the owner token header", name)
		}
	}
}
