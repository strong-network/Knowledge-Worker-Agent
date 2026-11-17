// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcpauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// stubStatuses stands in for the statuses opencode's server reports, for the
// duration of a test.
//
// The cleanup deliberately waits for any in-flight background refresh before
// restoring the stand-in. A refresh goroutine reads it when it runs, not when
// it is started, so restoring too early lets a leaked goroutine reach the
// previous stand-in and write its answer into the next test's cache.
func stubStatuses(t *testing.T, fn func(context.Context) (map[string]bool, error)) {
	t.Helper()
	old := connectorStatuses
	connectorStatuses = fn
	t.Cleanup(func() {
		waitForQuiescence()
		connectorStatuses = old
	})
}

func fixedStatuses(statuses map[string]bool) func(context.Context) (map[string]bool, error) {
	return func(context.Context) (map[string]bool, error) { return statuses, nil }
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

// twoServers is one connected server and one needing authentication — the
// two states the cache distinguishes.
var twoServers = map[string]bool{"obsidian": true, "atlassian": false}

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

// The batched endpoint must report every server from a single status read,
// preserving the connected/needs-auth distinction. This is the whole
// point of the endpoint: the connectors modal derives its status pills and
// filter counts from one request instead of one per server.
func TestHandleAllStatus_ReportsEveryServerFromOneList(t *testing.T) {
	useTempDB(t)
	resetColdRefresh(t)
	stubStatuses(t, fixedStatuses(twoServers))

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

// A second read is served from the cache rather than asking the server again.
func TestHandleAllStatus_SecondReadIsCached(t *testing.T) {
	useTempDB(t)
	resetColdRefresh(t)
	stubStatuses(t, fixedStatuses(twoServers))

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

// ?fresh=1 must ask the server again and reflect a status that changed underneath the
// cache — this is what the sign-in sheet relies on to confirm success.
func TestHandleAllStatus_FreshBypassesCache(t *testing.T) {
	useTempDB(t)
	resetColdRefresh(t)
	stubStatuses(t, fixedStatuses(twoServers))

	if got := getAllStatus(t, ""); got.Servers["atlassian"].Authenticated {
		t.Fatal("precondition: atlassian should start unauthenticated")
	}

	// Atlassian has now signed in.
	stubStatuses(t, fixedStatuses(map[string]bool{"obsidian": true, "atlassian": true}))
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
	useTempDB(t)
	resetColdRefresh(t)
	stubStatuses(t, func(context.Context) (map[string]bool, error) { return nil, errors.New("down") })

	rec := httptest.NewRecorder()
	HandleAllStatus(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/status", nil))
	if body := rec.Body.String(); !contains(body, `"servers":{}`) {
		t.Errorf("empty status should encode as an object, got %s", body)
	}
}

// Concurrent callers must collapse into a single status read. Before this
// guard, every cached status read spawned its own refresh — opening the modal
// with sixteen servers meant sixteen concurrent reads computing the same
// answer.
func TestRefreshInBackground_CoalescesConcurrentCallers(t *testing.T) {
	useTempDB(t)
	waitForQuiescence()

	var calls atomic.Int32
	stubStatuses(t, func(context.Context) (map[string]bool, error) {
		calls.Add(1)
		time.Sleep(300 * time.Millisecond)
		return map[string]bool{"obsidian": true}, nil
	})

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

	if n := calls.Load(); n != 1 {
		t.Errorf("expected exactly 1 status read, got %d", n)
	}
}
