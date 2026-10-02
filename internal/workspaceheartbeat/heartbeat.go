// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package workspaceheartbeat reports running chat work to the workspace sidecar.
// It uses only the workspace-local Unix socket; no sidecar proxy is exposed.
package workspaceheartbeat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
)

const socketPath = "/var/strong-network/socks/sidecar-ipc"

// Start runs one serial heartbeat loop until ctx is cancelled or the returned
// stop function is called. Failures never block chat or server startup. Activity
// is sampled anew for every attempt, including retries, rather than replayed.
// A workspace counts as active when a chat task is running (the active
// callback) or when the web UI recently reported user interaction (UIActive).
// KWA_WORKSPACE_HEARTBEAT=false disables reporting outside a workspace or when
// workspace idle policy should apply even while a chat task is running.
func Start(ctx context.Context, active func() bool) func() {
	switch strings.ToLower(strings.TrimSpace(env.Get("KWA_WORKSPACE_HEARTBEAT"))) {
	case "0", "false", "no", "off":
		log.Print("[workspace-heartbeat] disabled by KWA_WORKSPACE_HEARTBEAT")
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	r := newReporter(socketPath)
	log.Printf("[workspace-heartbeat] started: socket=%s interval=%s timeout=%s ideType=IDE_TYPE_VSCODE", socketPath, r.interval, r.client.Timeout)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer r.client.CloseIdleConnections()
		r.run(ctx, active)
	}()
	return func() {
		cancel()
		<-done
	}
}

type reporter struct {
	client     *http.Client
	interval   time.Duration
	maxBackoff time.Duration
}

func newReporter(socket string) *reporter {
	transport := &http.Transport{
		// Do not use environment proxies: localhost is just the HTTP Host;
		// the Unix socket determines the destination.
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:    1,
		IdleConnTimeout: 30 * time.Second,
	}
	return &reporter{
		client: &http.Client{
			Transport: transport,
			Timeout:   5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		interval:   5 * time.Second,
		maxBackoff: time.Minute,
	}
}

func (r *reporter) send(ctx context.Context, active bool) error {
	body, err := json.Marshal(struct {
		Timestamp string `json:"timestamp"`
		WasActive bool   `json:"wasActive"`
		IDEType   string `json:"ideType"`
	}{strconv.FormatInt(time.Now().Unix(), 10), active, "IDE_TYPE_VSCODE"})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/internal/v1/heartbeat", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Bound draining unexpected bodies. Never log response bodies, which may
	// contain details supplied by the sidecar or central service.
	_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("sidecar returned HTTP %d", resp.StatusCode)
	}
	return readErr
}

func (r *reporter) run(ctx context.Context, active func() bool) {
	defer log.Print("[workspace-heartbeat] stopped")
	delay := r.interval
	var lastSuccessLog time.Time
	var lastActive bool
	var sampled bool
	failures := 0
	for ctx.Err() == nil {
		started := time.Now()
		taskActive, uiActive := active(), UIActive()
		wasActive := taskActive || uiActive
		changed := !sampled || wasActive != lastActive
		if changed {
			log.Printf("[workspace-heartbeat] activity: wasActive=%t (task keep-alive=%t, ui keep-alive=%t)", wasActive, taskActive, uiActive)
			lastActive, sampled = wasActive, true
		}
		err := r.send(ctx, wasActive)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			failures++
			delay = min(delay*2, r.maxBackoff)
			log.Printf("[workspace-heartbeat] failed: wasActive=%t consecutive_failures=%d error=%v; retrying in %s", wasActive, failures, err, delay)
		} else {
			if changed || lastSuccessLog.IsZero() || failures > 0 || time.Since(lastSuccessLog) >= time.Minute {
				log.Printf("[workspace-heartbeat] delivered: wasActive=%t elapsed=%s recovered_after_failures=%d", wasActive, time.Since(started).Round(time.Millisecond), failures)
				lastSuccessLog = time.Now()
			}
			failures = 0
			delay = r.interval
		}
		// Keep a five-second cadence on success without overlapping requests.
		// Failures wait the full backoff after the request completes.
		wait := delay
		if err == nil {
			wait = max(0, delay-time.Since(started))
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
