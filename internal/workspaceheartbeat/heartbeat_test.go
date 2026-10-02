// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package workspaceheartbeat

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func socketReporter(t *testing.T, handler http.HandlerFunc) *reporter {
	t.Helper()
	dir, err := os.MkdirTemp("", "hb-") // Unix socket paths have a small length limit.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "ipc")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	r := newReporter(socket)
	t.Cleanup(r.client.CloseIdleConnections)
	return r
}

func TestUnixHeartbeatContract(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	for _, active := range []bool{true, false} {
		t.Run(strconv.FormatBool(active), func(t *testing.T) {
			r := socketReporter(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != "POST" || req.URL.Path != "/internal/v1/heartbeat" || req.Host != "localhost" {
					t.Errorf("unexpected request: %s %s host=%s", req.Method, req.URL, req.Host)
				}
				if req.Header.Get("Content-Type") != "application/json" || req.Header.Get("Authorization") != "" {
					t.Errorf("unexpected headers: %v", req.Header)
				}
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body) != 3 || body["wasActive"] != active || body["ideType"] != "IDE_TYPE_VSCODE" {
					t.Errorf("unexpected payload: %v", body)
				}
				ts, ok := body["timestamp"].(string)
				seconds, err := strconv.ParseUint(ts, 10, 64)
				if !ok || err != nil || time.Since(time.Unix(int64(seconds), 0)).Abs() > 5*time.Second {
					t.Errorf("timestamp must be current Unix seconds as a string: %v", body["timestamp"])
				}
				w.Write([]byte("{}"))
			})
			if err := r.send(context.Background(), active); err != nil {
				t.Fatal(err)
			}
			if r.client.Timeout != 5*time.Second {
				t.Fatal("missing request timeout")
			}
		})
	}
}

func TestFailureStatusesAndRedirects(t *testing.T) {
	for _, status := range []int{302, 401, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls atomic.Int32
			r := socketReporter(t, func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "http://example.invalid/other")
				w.WriteHeader(status)
			})
			if err := r.send(context.Background(), true); err == nil {
				t.Fatal("expected failure")
			}
			if calls.Load() != 1 {
				t.Fatal("followed redirect")
			}
		})
	}
}

func TestTimeoutAndCancellation(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	r := socketReporter(t, func(w http.ResponseWriter, req *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-req.Context().Done():
		}
	})
	defer close(release)
	r.client.Timeout = 20 * time.Millisecond
	if err := r.send(context.Background(), true); err == nil {
		t.Fatal("timeout succeeded")
	}
	<-entered
	r.client.Timeout = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	done := make(chan struct{})
	go func() { r.run(ctx, func() bool { return true }); close(done) }()
	<-entered
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel request")
	}
}

func TestBackoffReevaluatesActivity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var active atomic.Bool
	active.Store(true)
	type attempt struct {
		at     time.Time
		active bool
	}
	requests := make(chan attempt, 5)
	var count atomic.Int32
	r := socketReporter(t, func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			WasActive bool `json:"wasActive"`
		}
		json.NewDecoder(req.Body).Decode(&body)
		requests <- attempt{time.Now(), body.WasActive}
		n := count.Add(1)
		if n <= 3 {
			active.Store(false) // task ended during outage; do not replay true
			w.WriteHeader(503)
		} else {
			w.Write([]byte("{}"))
		}
		if n == 5 {
			cancel()
		}
	})
	r.interval = 10 * time.Millisecond
	r.maxBackoff = 30 * time.Millisecond
	r.run(ctx, active.Load)
	if len(requests) != 5 {
		t.Fatalf("expected 5 requests, got %d", len(requests))
	}
	first := <-requests
	if !first.active {
		t.Fatal("first request should be active")
	}
	for i, minimum := range []time.Duration{20 * time.Millisecond, 30 * time.Millisecond, 30 * time.Millisecond, 9 * time.Millisecond} {
		next := <-requests
		if next.active {
			t.Fatal("replayed stale activity")
		}
		if next.at.Sub(first.at) < minimum {
			t.Fatalf("retry %d arrived too soon", i)
		}
		first = next
	}
}

func TestUnavailableSocketAndDisabled(t *testing.T) {
	r := newReporter(filepath.Join(t.TempDir(), "missing"))
	if err := r.send(context.Background(), true); err == nil {
		t.Fatal("missing socket succeeded")
	}
	t.Setenv("KWA_WORKSPACE_HEARTBEAT", "false")
	stop := Start(context.Background(), func() bool { t.Error("disabled worker sampled activity"); return true })
	stop()
	stop()
}
