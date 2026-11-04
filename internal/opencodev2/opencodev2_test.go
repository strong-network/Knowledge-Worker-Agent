// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodev2

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
)

// The test binary doubles as a fake opencode 2.x when fakeEnv is set.
const fakeEnv = "KWA_TEST_FAKE_OPENCODE"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeEnv); mode != "" {
		os.Exit(fakeOpencode(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeOpencode answers --version, serves the few API routes KWA uses, and for
// any other command prints its arguments, its stdin, its PWD and whether it got
// the password.
func fakeOpencode(mode string, args []string) int {
	if len(args) > 0 && args[0] == "--version" {
		fmt.Println("opencode v2.2.5")
		return 0
	}
	if len(args) > 0 && args[0] == "serve" {
		if mode == "serve-fails" {
			fmt.Fprintln(os.Stderr, "Error: something broke")
			return 1
		}
		port := ""
		for i, a := range args {
			if a == "--port" && i+1 < len(args) {
				port = args[i+1]
			}
		}
		password := os.Getenv("OPENCODE_PASSWORD")
		started := time.Now()
		// Until this long after start, 2.x lists no connectors, as on a large
		// configuration.
		slow, _ := time.ParseDuration(os.Getenv("KWA_TEST_FAKE_SLOW_LIST"))
		var mcpCalls atomic.Int32
		var folderMu sync.Mutex
		folderCalls := map[string]int{}
		record := os.Getenv("KWA_TEST_FAKE_RECORD")
		mux := http.NewServeMux()
		mux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) })
		mux.HandleFunc("/api/model", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"data":[{"id":"claude-haiku-4.5","providerID":"github-copilot","variants":[{"id":"high"},{"id":"max"}]}]}`)
		})
		mux.HandleFunc("/api/mcp", func(w http.ResponseWriter, r *http.Request) {
			// A new folder lists no connectors at first, as 2.2.5 does, then
			// one connecting, then connected.
			if dir := r.URL.Query().Get("location[directory]"); dir != "" {
				folderMu.Lock()
				folderCalls[dir]++
				n := folderCalls[dir]
				if record != "" {
					if f, err := os.OpenFile(record, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
						fmt.Fprintln(f, dir)
						f.Close()
					}
				}
				folderMu.Unlock()
				switch n {
				case 1:
					fmt.Fprint(w, `{"data":[]}`)
				case 2:
					fmt.Fprint(w, `{"data":[{"name":"a","status":{"status":"pending"}}]}`)
				default:
					fmt.Fprint(w, `{"data":[{"name":"a","status":{"status":"connected"}}]}`)
				}
				return
			}
			if time.Since(started) < slow {
				fmt.Fprint(w, `{"data":[]}`)
				return
			}
			status := "connected"
			if mcpCalls.Add(1) <= 3 {
				status = "pending"
			}
			// Right after a reload 2.x answers an empty list once; a test asks
			// for that by creating this file.
			if p := os.Getenv("KWA_TEST_FAKE_EMPTY_ONCE"); p != "" {
				if os.Remove(p) == nil {
					fmt.Fprint(w, `{"data":[]}`)
					return
				}
			}
			fmt.Fprintf(w, `{"data":[{"name":"a","integrationID":"mcp_a","status":{"status":%q}},`+
				`{"name":"b","integrationID":"mcp_b","status":{"status":"needs_auth"}},`+
				`{"name":"off","integrationID":"mcp_off","status":{"status":"disabled"}}]}`, status)
		})
		// Credentials, kept in memory. With KWA_TEST_FAKE_CREDS set, the store
		// is written there, values and all, after every change.
		var credMu sync.Mutex
		var creds []map[string]any
		nextID := 0
		dump := func() {
			if p := os.Getenv("KWA_TEST_FAKE_CREDS"); p != "" {
				b, _ := json.Marshal(creds)
				_ = os.WriteFile(p, b, 0o600)
			}
		}
		mux.HandleFunc("/api/credential", func(w http.ResponseWriter, r *http.Request) {
			credMu.Lock()
			defer credMu.Unlock()
			switch r.Method {
			case http.MethodGet:
				b, _ := json.Marshal(map[string]any{"data": creds})
				w.Write(b)
			case http.MethodPost:
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				nextID++
				c := map[string]any{"id": fmt.Sprintf("cred_%d", nextID), "integrationID": body["integrationID"],
					"label": body["label"], "active": true, "value": body["value"]}
				creds = append(creds, c)
				dump()
				b, _ := json.Marshal(map[string]any{"data": c})
				w.Write(b)
			}
		})
		mux.HandleFunc("DELETE /api/credential/{id}", func(w http.ResponseWriter, r *http.Request) {
			credMu.Lock()
			defer credMu.Unlock()
			kept := creds[:0:0]
			for _, c := range creds {
				if c["id"] != r.PathValue("id") {
					kept = append(kept, c)
				}
			}
			creds = kept
			dump()
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("/api/session/", func(w http.ResponseWriter, r *http.Request) {
			if record != "" {
				_ = os.WriteFile(record, []byte(r.Method+" "+r.URL.Path), 0o644)
			}
			w.WriteHeader(http.StatusNoContent)
		})
		auth := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u, p, ok := r.BasicAuth(); !ok || u != "opencode" || p != password {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			mux.ServeHTTP(w, r)
		})
		if err := http.ListenAndServe("127.0.0.1:"+port, auth); err != nil {
			return 1
		}
		return 0
	}
	stdin, _ := io.ReadAll(os.Stdin)
	out, _ := json.Marshal(map[string]any{"args": args, "password": os.Getenv("OPENCODE_PASSWORD") != "",
		"stdin": string(stdin), "pwd": os.Getenv("PWD")})
	if record := os.Getenv("KWA_TEST_FAKE_RECORD"); record != "" && len(args) > 0 && args[0] == "reload" {
		_ = os.WriteFile(record, out, 0o644)
	}
	fmt.Println(string(out))
	return 0
}

func useFake(t *testing.T, mode string) {
	t.Helper()
	t.Setenv(fakeEnv, mode)
	t.Setenv("KWA_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	prevBin, prevWS := config.OpencodeBin, config.Workspace
	config.OpencodeBin, config.Workspace = os.Args[0], t.TempDir()
	Detect(config.OpencodeBin)
	t.Cleanup(func() {
		Stop()
		srv.mu.Lock()
		srv.failedAt, srv.lastErr, srv.stopped = time.Time{}, nil, false
		srv.mu.Unlock()
		config.OpencodeBin, config.Workspace = prevBin, prevWS
	})
}

// A request arriving during shutdown must not start a server that would
// outlive KWA.
func TestNoServerAfterStop(t *testing.T) {
	useFake(t, "ok")
	Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := Ensure(ctx); err == nil {
		t.Fatal("Ensure started a server after Stop")
	}
	if _, _, ok := Running(); ok {
		t.Fatal("a server is running after Stop")
	}
}

func TestParseMajor(t *testing.T) {
	for in, want := range map[string]int{
		"opencode v2.2.5": 2,
		"1.19.4":          1,
		"v3.0.0-beta.1":   3,
		"":                1,
		"no version here": 1,
	} {
		if got := parseMajor(in); got != want {
			t.Errorf("parseMajor(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestMajorIsWhatDetectFound(t *testing.T) {
	majorMu.Lock()
	majorSeen = 0
	majorMu.Unlock()
	if got := Major(os.Args[0]); got != 1 {
		t.Fatalf("Major before Detect = %d, want 1", got)
	}
	useFake(t, "ok")
	if got := Major(config.OpencodeBin); got != 2 {
		t.Fatalf("Major = %d, want 2", got)
	}
	if !Enabled() {
		t.Fatal("Enabled() = false for a 2.x binary")
	}
}

// A chat's folder has its own connectors on 2.x; a turn waits for them, once.
func TestWarmFolderWaitsForTheFoldersConnectors(t *testing.T) {
	useFake(t, "ok")
	record := filepath.Join(t.TempDir(), "folders")
	t.Setenv("KWA_TEST_FAKE_RECORD", record)
	WarmFolder(context.Background(), "/chats/a")
	if _, err := os.Stat(record); err == nil {
		t.Fatal("WarmFolder asked a server that isn't running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, _, err := Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		raw, _ := os.ReadFile(record)
		return strings.Count(string(raw), "/chats/a\n")
	}
	WarmFolder(ctx, "/chats/a")
	if n := count(); n != 3 {
		t.Fatalf("asked %d times for a new folder, want 3 (empty, connecting, connected)", n)
	}
	WarmFolder(ctx, "/chats/a")
	if n := count(); n != 4 {
		t.Fatalf("asked %d times in all, want one more for a warm folder", n)
	}
	// With no connectors at all, an empty list is the answer.
	connectorCount.Store(0)
	start := time.Now()
	WarmFolder(ctx, "/chats/b")
	if took := time.Since(start); took > time.Second {
		t.Errorf("waited %s for a folder when there are no connectors", took)
	}
}

func TestScratchDirIsKeptUntilStop(t *testing.T) {
	useFake(t, "ok")
	a, err := ScratchDir()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := ScratchDir(); b != a {
		t.Fatalf("second call gave %q, want %q", b, a)
	}
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	c, err := ScratchDir()
	if err != nil || c == a {
		t.Fatalf("after its folder was removed: %q, %v", c, err)
	}
	Stop()
	if _, err := os.Stat(c); err == nil {
		t.Error("Stop left the scratch folder")
	}
}

func TestRunMessageMovesOnlyARunsMessage(t *testing.T) {
	for _, tc := range []struct{ args, rest, message string }{
		{"run --format json -- hello", "run --format json", "hello"},
		{"session delete -- ses_1", "session delete -- ses_1", ""},
		{"run --format json", "run --format json", ""},
		{"run -- two words", "run -- two words", ""},
	} {
		rest, message, _ := runMessage(strings.Fields(tc.args))
		if strings.Join(rest, " ") != tc.rest || message != tc.message {
			t.Errorf("%q: rest %q message %q", tc.args, strings.Join(rest, " "), message)
		}
	}
}

func TestWithFlagsGoesBeforeTheEndOfOptions(t *testing.T) {
	got := withFlags([]string{"run", "--format", "json", "--", "--not-a-flag"}, []string{"--server", "U"})
	want := "run --format json --server U -- --not-a-flag"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %q, want %q", strings.Join(got, " "), want)
	}
	got = withFlags([]string{"session", "delete", "ses_1"}, []string{"--standalone"})
	if strings.Join(got, " ") != "session delete ses_1 --standalone" {
		t.Fatalf("got %q", strings.Join(got, " "))
	}
}

func TestCommandGoesToTheServerOnceItsConnectorsHaveConnected(t *testing.T) {
	useFake(t, "ok")
	// KWA's own folder must not reach opencode: 2.x would work there.
	t.Setenv("PWD", "/where/kwa/started")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	message := `Say "hi" -- twice`
	out, err := Command(ctx, config.OpencodeBin, "run", "--format", "json", "--", message).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Args     []string `json:"args"`
		Password bool     `json:"password"`
		Stdin    string   `json:"stdin"`
		PWD      string   `json:"pwd"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	url, _, ok := Running()
	if !ok {
		t.Fatal("the server isn't running")
	}
	want := "run --format json --server " + url
	if strings.Join(got.Args, " ") != want || !got.Password {
		t.Fatalf("got %q (password %v), want %q with the password", strings.Join(got.Args, " "), got.Password, want)
	}
	if got.Stdin != message {
		t.Errorf("stdin = %q, want the message %q as it is", got.Stdin, message)
	}
	if got.PWD != "" {
		t.Errorf("opencode got PWD=%q", got.PWD)
	}
	var mcp struct {
		Data []struct {
			Status struct{ Status string } `json:"status"`
		} `json:"data"`
	}
	if err := Get(ctx, "/api/mcp", &mcp); err != nil {
		t.Fatal(err)
	}
	if mcp.Data[0].Status.Status != "connected" {
		t.Fatalf("Ensure returned before the connectors connected: %+v", mcp.Data)
	}
}

func TestCommandRunsAPrivateServerWhenKWAsWontStart(t *testing.T) {
	useFake(t, "serve-fails")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := Command(ctx, config.OpencodeBin, "session", "delete", "ses_1").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"session","delete","ses_1","--standalone"`) || strings.Contains(string(out), `"password":true`) {
		t.Fatalf("got %s", out)
	}
	if _, _, err := Ensure(ctx); err == nil || !strings.Contains(err.Error(), "something broke") {
		t.Fatalf("Ensure error = %v, want the server's last line", err)
	}
}

func TestTheServerStartsAgainAfterItExits(t *testing.T) {
	useFake(t, "ok")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first, _, err := Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	_ = srv.cmd.Process.Kill()
	exited := srv.exited
	srv.mu.Unlock()
	<-exited
	second, _, err := Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatalf("Ensure returned the dead server's address %s", first)
	}
}

func TestInterruptAsksTheServerToStopTheTurn(t *testing.T) {
	useFake(t, "ok")
	record := filepath.Join(t.TempDir(), "request")
	t.Setenv("KWA_TEST_FAKE_RECORD", record)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, _, err := Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Interrupt(ctx, "ses_abc"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(record)
	if string(got) != "POST /api/session/ses_abc/interrupt" {
		t.Fatalf("server saw %q", got)
	}
}

func TestInterruptWithoutAServerDoesNothing(t *testing.T) {
	useFake(t, "ok")
	if err := Interrupt(context.Background(), "ses_abc"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Running(); ok {
		t.Fatal("Interrupt started a server")
	}
}
