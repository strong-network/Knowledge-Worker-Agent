// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcpauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// stubOpencode points the package at a fake opencode for the duration of a
// test.
//
// The cleanup deliberately waits for any in-flight background refresh before
// restoring the global. A refresh goroutine resolves the binary when it runs,
// not when it is started, so restoring too early lets a leaked goroutine fall
// through resolveBin() to the developer's REAL opencode — which then connects
// to every configured MCP server and blocks for up to commandTimeout. That is a
// test-only hazard, but it makes unrelated tests hang, so it is closed here
// rather than worked around at each call site.
func stubOpencode(t *testing.T, script string) {
	t.Helper()
	old := OpencodeBin
	OpencodeBin = writeFakeOpencode(t, script)
	t.Cleanup(func() {
		waitForQuiescence()
		OpencodeBin = old
	})
}

// waitForQuiescence blocks until no background refresh is running, or gives up.
// Best-effort: callers use it to avoid leaking work into the next test.
func waitForQuiescence() {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !refreshRunning.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// resetColdRefresh clears the once-per-process cold-cache latch so each test
// starts from the same state regardless of run order.
func resetColdRefresh(t *testing.T) {
	t.Helper()
	waitForQuiescence()
	coldRefreshAttempted.Store(false)
	t.Cleanup(func() { coldRefreshAttempted.Store(false) })
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func countLines(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// waitUntil polls cond for up to two seconds. Used instead of a fixed sleep so
// the test is neither flaky on a slow machine nor slow on a fast one.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

// useTempDB points the db package at a throwaway SQLite file. The status cache
// is a real table, so the batched endpoint cannot be exercised against the
// nil-DB default the other tests in this package run with.
func useTempDB(t *testing.T) {
	t.Helper()
	f, err := os.CreateTemp("", "mcpstatus-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() {
		db.Close()
		os.Remove(path)
	})
	if err := db.Init(path); err != nil {
		t.Fatal(err)
	}
}

// listStub is a fake opencode whose `mcp list` reports one connected server and
// one needing authentication — the two states the cache distinguishes.
const listStub = `#!/bin/sh
if [ "$1" = "mcp" ] && [ "$2" = "list" ]; then
  printf '%s\n' '●  ✓ obsidian connected'
  printf '%s\n' '●  ⚠ atlassian needs authentication'
  exit 0
fi
exit 1
`

type allStatusResponse struct {
	Servers map[string]struct {
		Authenticated bool `json:"authenticated"`
	} `json:"servers"`
	Cached bool `json:"cached"`
}

func getAllStatus(t *testing.T, query string) allStatusResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleAllStatus(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/status"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status code %d, body %s", rec.Code, rec.Body.String())
	}
	var got allStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
	}
	return got
}

// The batched endpoint must report every server from a single `opencode mcp
// list`, preserving the connected/needs-auth distinction. This is the whole
// point of the endpoint: the connectors modal derives its status pills and
// filter counts from one request instead of one per server.
func TestHandleAllStatus_ReportsEveryServerFromOneList(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	useTempDB(t)
	resetColdRefresh(t)
	stubOpencode(t, listStub)

	got := getAllStatus(t, "")
	if len(got.Servers) != 2 {
		t.Fatalf("expected 2 servers, got %d: %+v", len(got.Servers), got.Servers)
	}
	if !got.Servers["obsidian"].Authenticated {
		t.Error("obsidian should be authenticated (connected)")
	}
	if got.Servers["atlassian"].Authenticated {
		t.Error("atlassian should NOT be authenticated (needs authentication)")
	}
}

// A second read is served from the cache rather than re-running the CLI. The
// stub is swapped for one that fails if invoked synchronously, so a cache miss
// would surface as an empty response.
func TestHandleAllStatus_SecondReadIsCached(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	useTempDB(t)
	resetColdRefresh(t)
	stubOpencode(t, listStub)

	if first := getAllStatus(t, ""); first.Cached {
		t.Error("first read on a cold cache should not be reported as cached")
	}

	second := getAllStatus(t, "")
	if !second.Cached {
		t.Error("second read should be served from the cache")
	}
	if !second.Servers["obsidian"].Authenticated {
		t.Errorf("cached read lost the status: %+v", second.Servers)
	}
}

// ?fresh=1 must re-run the CLI and reflect a status that changed underneath the
// cache — this is what the sign-in sheet relies on to confirm success.
func TestHandleAllStatus_FreshBypassesCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	useTempDB(t)
	resetColdRefresh(t)
	stubOpencode(t, listStub)

	if got := getAllStatus(t, ""); got.Servers["atlassian"].Authenticated {
		t.Fatal("precondition: atlassian should start unauthenticated")
	}

	// Atlassian has now signed in.
	stubOpencode(t, `#!/bin/sh
if [ "$1" = "mcp" ] && [ "$2" = "list" ]; then
  printf '%s\n' '●  ✓ obsidian connected'
  printf '%s\n' '●  ✓ atlassian connected'
  exit 0
fi
exit 1
`)
	got := getAllStatus(t, "?fresh=1")
	if !got.Servers["atlassian"].Authenticated {
		t.Errorf("fresh read should see the new status: %+v", got.Servers)
	}
	if got.Cached {
		t.Error("fresh read should not be reported as cached")
	}
}

// An empty result must still encode as an object, never null, so the frontend
// can index it without a guard on every lookup.
func TestHandleAllStatus_EmptyEncodesAsObject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	useTempDB(t)
	resetColdRefresh(t)
	stubOpencode(t, "#!/bin/sh\nexit 1\n")

	rec := httptest.NewRecorder()
	HandleAllStatus(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/status", nil))
	if body := rec.Body.String(); !contains(body, `"servers":{}`) {
		t.Errorf("empty status should encode as an object, got %s", body)
	}
}

// Concurrent callers must collapse into a single `opencode mcp list`. Before
// this guard, every cached status read spawned its own refresh — opening the
// modal with sixteen servers meant sixteen concurrent CLI processes computing
// the same answer.
func TestRefreshInBackground_CoalescesConcurrentCallers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	useTempDB(t)
	waitForQuiescence()

	// The stub appends a line per invocation so we can count them.
	counter := t.TempDir() + "/calls"
	stubOpencode(t, `#!/bin/sh
if [ "$1" = "mcp" ] && [ "$2" = "list" ]; then
  echo x >> `+counter+`
  sleep 0.3
  printf '%s\n' '●  ✓ obsidian connected'
  exit 0
fi
exit 1
`)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			refreshInBackground()
		}()
	}
	wg.Wait()

	// Wait for whichever refresh won the race to finish.
	waitUntil(t, func() bool { return !refreshRunning.Load() })

	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("stub never ran: %v", err)
	}
	if n := countLines(string(data)); n != 1 {
		t.Errorf("expected exactly 1 `mcp list` invocation, got %d", n)
	}
}
