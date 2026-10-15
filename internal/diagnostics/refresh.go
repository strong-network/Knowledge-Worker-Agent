// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package diagnostics

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/materializer"
)

// RefreshState is the progress of a configuration refresh.
type RefreshState struct {
	Running    bool       `json:"running"`
	Step       string     `json:"step,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	OK         bool       `json:"ok"`
	Error      string     `json:"error,omitempty"`
}

// ConfigRefresher fetches and re-applies the central configuration, and
// repeats the startup steps that depend on it. It calls step as it goes.
type ConfigRefresher func(step func(string)) error

var refresh struct {
	sync.Mutex
	fn    ConfigRefresher
	state *RefreshState
}

// SetConfigRefresher installs the refresh. Call it once at startup.
func SetConfigRefresher(fn ConfigRefresher) {
	refresh.Lock()
	defer refresh.Unlock()
	refresh.fn = fn
}

var (
	errRefreshBusy     = errors.New("a refresh is already running")
	errRefreshStarting = errors.New("Knowledge Worker Agent is still starting; try again in a moment")
	errRefreshNothing  = errors.New("this workspace has no central configuration to refresh")
	errRefreshNone     = errors.New("refreshing isn't available")
)

// StartRefresh starts a refresh in the background and returns its state.
func StartRefresh() (RefreshState, error) {
	refresh.Lock()
	defer refresh.Unlock()
	switch {
	case refresh.state != nil && refresh.state.Running:
		return *refresh.state, errRefreshBusy
	case refresh.fn == nil:
		return RefreshState{}, errRefreshNone
	case src.Status != nil && src.Status() == "starting":
		return RefreshState{}, errRefreshStarting
	case strings.TrimSpace(env.Get(materializer.EnvProjectID)) == "" || strings.TrimSpace(env.Get(materializer.EnvConfigRepo)) == "":
		return RefreshState{}, errRefreshNothing
	}
	st := &RefreshState{Running: true, StartedAt: time.Now(), Step: "Starting"}
	refresh.state = st
	go runRefresh(refresh.fn, st)
	return *st, nil
}

func runRefresh(fn ConfigRefresher, st *RefreshState) {
	var err error
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the refresh stopped unexpectedly: %v", r)
		}
		refresh.Lock()
		defer refresh.Unlock()
		now := time.Now()
		st.Running, st.Step, st.FinishedAt, st.OK = false, "", &now, err == nil
		if err != nil {
			st.Error = Redact(err.Error())
		}
	}()
	err = fn(func(step string) {
		refresh.Lock()
		st.Step = step
		refresh.Unlock()
	})
}

// CurrentRefresh returns the latest refresh's state, or nil when there was none.
func CurrentRefresh() *RefreshState {
	refresh.Lock()
	defer refresh.Unlock()
	if refresh.state == nil {
		return nil
	}
	st := *refresh.state
	return &st
}
