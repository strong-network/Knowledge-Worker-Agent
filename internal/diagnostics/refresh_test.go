// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package diagnostics

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func withRefresher(t *testing.T, fn ConfigRefresher) {
	t.Helper()
	t.Setenv("KWA_PROJECT_ID", "proj_team")
	t.Setenv("KWA_CONFIG_REPO_URL", "https://example.com/config.git")
	Configure(Sources{Status: func() string { return "ready" }})
	refresh.Lock()
	refresh.state = nil
	refresh.Unlock()
	SetConfigRefresher(fn)
	t.Cleanup(func() { SetConfigRefresher(nil) })
}

func waitDone(t *testing.T) RefreshState {
	t.Helper()
	for i := 0; i < 200; i++ {
		if st := CurrentRefresh(); st != nil && !st.Running {
			return *st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("refresh did not finish")
	return RefreshState{}
}

func TestRefreshRunsInTheBackgroundOneAtATime(t *testing.T) {
	release := make(chan struct{})
	withRefresher(t, func(step func(string)) error {
		step("Fetching and applying the configuration")
		<-release
		step("Updating connectors")
		return nil
	})
	st, err := StartRefresh()
	if err != nil || !st.Running {
		t.Fatalf("start: %+v, %v", st, err)
	}
	if _, err := StartRefresh(); !errors.Is(err, errRefreshBusy) {
		t.Fatalf("second start: %v, want busy", err)
	}
	for i := 0; i < 100 && CurrentRefresh().Step != "Fetching and applying the configuration"; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if got := CurrentRefresh(); !got.Running || got.Step != "Fetching and applying the configuration" {
		t.Fatalf("while running: %+v", got)
	}
	close(release)
	done := waitDone(t)
	if !done.OK || done.Error != "" || done.Step != "" || done.FinishedAt == nil {
		t.Fatalf("done: %+v", done)
	}
	if _, err := StartRefresh(); err != nil {
		t.Fatalf("a finished refresh must not block the next: %v", err)
	}
	waitDone(t)
}

func TestRefreshRecordsFailuresRedacted(t *testing.T) {
	withRefresher(t, func(func(string)) error {
		return errors.New("clone https://alex:tok123@example.com/config.git: exit status 128")
	})
	if _, err := StartRefresh(); err != nil {
		t.Fatal(err)
	}
	done := waitDone(t)
	if done.OK || !strings.Contains(done.Error, "exit status 128") || strings.Contains(done.Error, "tok123") {
		t.Fatalf("failure: %+v", done)
	}
}

func TestRefreshSurvivesAPanic(t *testing.T) {
	withRefresher(t, func(func(string)) error { panic("boom") })
	if _, err := StartRefresh(); err != nil {
		t.Fatal(err)
	}
	if done := waitDone(t); done.OK || !strings.Contains(done.Error, "boom") {
		t.Fatalf("panic: %+v", done)
	}
}

func TestRefreshIsRefusedWhenItCantRun(t *testing.T) {
	withRefresher(t, func(func(string)) error { return nil })
	Configure(Sources{Status: func() string { return "starting" }})
	if _, err := StartRefresh(); !errors.Is(err, errRefreshStarting) {
		t.Fatalf("while starting: %v", err)
	}
	Configure(Sources{Status: func() string { return "ready" }})
	t.Setenv("KWA_PROJECT_ID", "")
	t.Setenv("SDS_PROJECT_ID", "")
	if _, err := StartRefresh(); !errors.Is(err, errRefreshNothing) {
		t.Fatalf("no project: %v", err)
	}
	if CurrentRefresh() != nil {
		t.Fatal("a refused refresh must not record a state")
	}
}

func TestRefreshRoutes(t *testing.T) {
	release := make(chan struct{})
	withRefresher(t, func(func(string)) error { <-release; return nil })
	rec := httptest.NewRecorder()
	HandleRefreshStart(rec, httptest.NewRequest("POST", "/api/diagnostics/refresh-config", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	HandleRefreshStart(rec, httptest.NewRequest("POST", "/api/diagnostics/refresh-config", nil))
	var busy struct {
		Error   string        `json:"error"`
		Refresh *RefreshState `json:"refresh"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &busy); err != nil || rec.Code != http.StatusConflict || busy.Error == "" || busy.Refresh == nil || !busy.Refresh.Running {
		t.Fatalf("busy: %d %s", rec.Code, rec.Body)
	}
	close(release)
	waitDone(t)
	rec = httptest.NewRecorder()
	HandleRefreshState(rec, httptest.NewRequest("GET", "/api/diagnostics/refresh-config", nil))
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("state: %s", rec.Body)
	}
}
