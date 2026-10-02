// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeserver

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
)

// turnStartupTimeout bounds how long RunTurn waits for the FIRST event of a
// turn before giving up. Mirrors the CLI runner's KWA_OPENCODE_STARTUP_TIMEOUT
// (bare seconds or a Go duration; <=0 effectively disables it).
func turnStartupTimeout() time.Duration {
	raw := strings.TrimSpace(env.Get("KWA_OPENCODE_STARTUP_TIMEOUT"))
	if raw == "" {
		return 90 * time.Second
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d <= 0 {
			return 24 * time.Hour
		}
		return d
	}
	if n, err := strconv.Atoi(raw); err == nil {
		if n <= 0 {
			return 24 * time.Hour
		}
		return time.Duration(n) * time.Second
	}
	return 90 * time.Second
}

// PermissionRegistry lets the HTTP answer handler resolve a pending permission
// request to the client + session that can respond to it. A single global
// registry is shared by all in-flight turns.
type PermissionRegistry struct {
	mu      sync.Mutex
	entries map[string]permEntry // keyed by app session id
}

type permEntry struct {
	client          *Client
	opencodeSession string
}

var Permissions = &PermissionRegistry{entries: map[string]permEntry{}}

func (r *PermissionRegistry) set(appSession string, c *Client, opencodeSession string) {
	r.mu.Lock()
	r.entries[appSession] = permEntry{client: c, opencodeSession: opencodeSession}
	r.mu.Unlock()
}

func (r *PermissionRegistry) clear(appSession string) {
	r.mu.Lock()
	delete(r.entries, appSession)
	r.mu.Unlock()
}

// Respond answers a pending permission for the given app session id. response
// is one of "once", "always", "reject". Returns false if no in-flight turn is
// registered for the session.
func (r *PermissionRegistry) Respond(ctx context.Context, appSession, permissionID, response string) (bool, error) {
	r.mu.Lock()
	e, ok := r.entries[appSession]
	r.mu.Unlock()
	if !ok {
		return false, nil
	}
	if err := e.client.RespondPermission(ctx, e.opencodeSession, permissionID, response); err != nil {
		return true, err
	}
	return true, nil
}

// TurnRequest describes a single chat turn to run against the server.
type TurnRequest struct {
	AppSessionID    string // this app's session id (browser-facing)
	OpencodeSession string // resume this opencode session id (empty = create new)
	Workdir         string // desired working directory for the server
	FallbackDir     string // used when Workdir is empty/invalid
	Prompt          string // fully-composed prompt text (preamble already applied)
	Title           string // title for a newly-created session

	ProviderID string // e.g. "github-copilot" (empty = server default)
	ModelID    string // e.g. "claude-sonnet-4.6"
	Agent      string // opencode agent name (empty = default)
	Variant    string // reasoning variant (empty = none)
}

// TurnResult carries the streamed events plus a late-bound opencode session id.
type TurnResult struct {
	Events <-chan Event
	sessMu sync.Mutex
	sessID string
	client *Client // the server client bound to this turn (for Abort)
}

// SessionID returns the opencode session id used for this turn (available once
// the turn has started). Safe for concurrent use.
func (t *TurnResult) SessionID() string {
	t.sessMu.Lock()
	defer t.sessMu.Unlock()
	return t.sessID
}

// Client returns the opencode server client bound to this turn. Callers use it
// to Abort the running session directly, without re-Ensuring a server (which
// could otherwise spawn a fresh process during teardown). May be nil if the
// turn failed before a client was resolved.
func (t *TurnResult) Client() *Client {
	return t.client
}

func (t *TurnResult) setSessionID(id string) {
	t.sessMu.Lock()
	t.sessID = id
	t.sessMu.Unlock()
}

// RunTurn ensures a server for the workdir, (re)creates the session if needed,
// subscribes to the event stream, sends the prompt, and streams neutral events
// until the session goes idle. The returned channel is closed when the turn
// completes. A terminal KindResult event (with accumulated tokens/cost) is
// emitted before close, mirroring the CLI runner's synthesized result.
func (s *Supervisor) RunTurn(ctx context.Context, req TurnRequest) (*TurnResult, error) {
	client, _, err := s.Ensure(ctx, req.Workdir, req.FallbackDir)
	if err != nil {
		return nil, err
	}

	// Resolve the opencode session: reuse the persisted one if it still exists
	// on this server, otherwise create a fresh session.
	opencodeSession := req.OpencodeSession
	if opencodeSession == "" || !client.SessionExists(ctx, opencodeSession) {
		created, cerr := client.CreateSession(ctx, req.Title)
		if cerr != nil {
			return nil, cerr
		}
		opencodeSession = created
	}

	return runTurnWith(ctx, client, opencodeSession, req, turnStartupTimeout()), nil
}

// runTurnWith drives the streaming turn against an already-resolved client and
// opencode session. It is separated from RunTurn (which owns Ensure + session
// resolution, both of which touch real processes) so the orchestration — event
// streaming, the inactivity watchdog, prompt error handling, and abort — can be
// tested against a fake HTTP server. timeout bounds both time-to-first-event
// and inter-event inactivity.
func runTurnWith(ctx context.Context, client *Client, opencodeSession string, req TurnRequest, timeout time.Duration) *TurnResult {
	result := &TurnResult{}
	result.setSessionID(opencodeSession)
	result.client = client
	ch := make(chan Event, 64)
	result.Events = ch

	// Register for permission answers and ensure cleanup.
	Permissions.set(req.AppSessionID, client, opencodeSession)

	// The subscription must outlive the prompt call, so run it in its own
	// context we cancel when the turn ends.
	streamCtx, cancel := context.WithCancel(ctx)

	go func() {
		defer func() {
			cancel()
			Permissions.clear(req.AppSessionID)
			close(ch)
		}()

		// Accumulators for the synthesized terminal result event.
		var (
			tokensIn      int
			tokensOut     int
			tokensInKnown bool
			cost          float64
			sawIdle       bool
		)

		// Subscribe in a goroutine; classified events (plus a KindResult
		// sentinel on session.idle) arrive on raw. subErr carries the
		// subscription's terminal error, if any. ready is closed once the SSE
		// stream is established (the server's guaranteed first frame is
		// "server.connected"), so the prompt is sent only after we're
		// subscribed and cannot miss the earliest events.
		raw := make(chan Event, 64)
		subErr := make(chan error, 1)
		ready := make(chan struct{})
		go func() {
			st := newTurnState()
			scope := newSessionScope(opencodeSession)
			readyOnce := false
			err := client.Subscribe(streamCtx, func(ev ServerEvent) bool {
				if !readyOnce {
					readyOnce = true
					close(ready)
				}
				// Learn parent→child session lineage BEFORE scope filtering: a
				// subagent (`task` tool) runs in its own child session, first
				// introduced by a session event whose sessionID is not yet in
				// scope.
				scope.observe(ev)
				sid := strProp(ev.Properties, "sessionID")
				if sid != "" && !scope.contains(sid) {
					return true // not our turn
				}
				events, done := classifyEvent(ev, st)
				if sid != "" && !scope.isRoot(sid) {
					// Subagent child session: keep the turn alive and show
					// progress, but don't render its internal steps as
					// top-level rows, and never let a child's idle end the
					// parent turn (Option A).
					events = demoteChildEvents(events, opencodeSession)
					done = false
				}
				for _, e := range events {
					select {
					case raw <- e:
					case <-streamCtx.Done():
						return false
					}
				}
				if done {
					select {
					case raw <- Event{Kind: KindResult, SessionID: opencodeSession}:
					case <-streamCtx.Done():
					}
					return false
				}
				return true
			})
			subErr <- err
		}()

		// Send the prompt (async) from its own goroutine once the subscription
		// is established. The result is reported via promptErr (buffered) and
		// consumed by the loop below; this goroutine must NEVER write to ch
		// directly — the loop owns ch, and writing here would race with the
		// deferred close(ch) (send on closed channel panic) when the consumer
		// exits first (e.g. client abort).
		promptErr := make(chan error, 1)
		go func() {
			// Wait for the stream to be live so we can't miss the earliest
			// events, but don't block forever if the first frame is slow. We
			// intentionally do NOT bail on streamCtx cancellation here: the
			// prompt must be sent once the stream is up, otherwise a turn whose
			// stream races to completion could skip the prompt entirely. If the
			// turn is already cancelled, PromptAsync fails fast on its own.
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
			}
			pr := PromptRequest{Parts: []map[string]any{TextPart(req.Prompt)}}
			if req.ProviderID != "" && req.ModelID != "" {
				pr.Model = &PromptModel{ProviderID: req.ProviderID, ModelID: req.ModelID}
			}
			pr.Agent = req.Agent
			pr.Variant = req.Variant
			promptErr <- client.PromptAsync(streamCtx, opencodeSession, pr)
		}()

		// Consume events. The timer bounds time-to-first-event AND, once events
		// flow, inter-event inactivity: opencode may keep the stream open but
		// go silent (never emitting session.idle) if it hangs, so we reset the
		// timer on every event and treat a subsequent expiry as a stalled turn
		// rather than blocking forever.
		gotFirst := false
		watchdog := time.NewTimer(timeout)
		defer watchdog.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case err := <-promptErr:
				if err != nil {
					ch <- Event{Kind: KindError, Text: errString("send prompt", err)}
					return
				}
				// Success: nothing to do; keep consuming events.
			case <-watchdog.C:
				if !gotFirst {
					ch <- Event{Kind: KindError, Text: "opencode produced no events for this turn (check authentication and model)"}
				} else {
					log.Printf("[opencode-serve] session=%s turn stalled (no events for %s)", req.AppSessionID, timeout)
					ch <- Event{Kind: KindError, Text: "opencode stopped responding mid-turn"}
				}
				return
			case err := <-subErr:
				// Subscription ended (stream closed/errored). The idle sentinel
				// (KindResult) and the stream-close signal race, so drain any
				// events already buffered on raw before deciding — otherwise a
				// clean idle that arrived just before EOF would be misreported
				// as "ended without session.idle".
				for drained := true; drained; {
					select {
					case e := <-raw:
						gotFirst = true
						switch e.Kind {
						case KindStepFinish:
							if e.TokensInKnown {
								tokensInKnown = true
							}
							tokensIn += e.TokensIn
							tokensOut += e.TokensOut
							cost += e.Cost
						case KindResult:
							sawIdle = true
						default:
							ch <- e
						}
					default:
						drained = false
					}
				}
				// If we never saw idle, surface a soft error only when nothing
				// streamed at all.
				if err != nil && !gotFirst {
					ch <- Event{Kind: KindError, Text: errString("event stream", err)}
					return
				}
				goto finish
			case e := <-raw:
				gotFirst = true
				// Reset the inactivity watchdog on every event.
				if !watchdog.Stop() {
					select {
					case <-watchdog.C:
					default:
					}
				}
				watchdog.Reset(timeout)
				switch e.Kind {
				case KindStepFinish:
					if e.TokensInKnown {
						tokensInKnown = true
					}
					tokensIn += e.TokensIn
					tokensOut += e.TokensOut
					cost += e.Cost
					continue // folded into terminal result
				case KindResult:
					sawIdle = true
					goto finish
				default:
					ch <- e
				}
			}
		}

	finish:
		if !sawIdle {
			log.Printf("[opencode-serve] session=%s turn ended without session.idle", req.AppSessionID)
		}
		ch <- Event{
			Kind:          KindResult,
			SessionID:     opencodeSession,
			TokensIn:      tokensIn,
			TokensOut:     tokensOut,
			TokensInKnown: tokensInKnown,
			Cost:          cost,
		}
	}()

	return result
}

// PrepareModel splits a combined "provider/model" identifier (as stored in
// SessionConfig.Model, e.g. "github-copilot/claude-sonnet-4.6") into its
// providerID and modelID. If there is no slash, both are returned empty so the
// caller falls back to the server's default model.
func PrepareModel(combined string) (providerID, modelID string) {
	combined = strings.TrimSpace(combined)
	if combined == "" {
		return "", ""
	}
	idx := strings.Index(combined, "/")
	if idx <= 0 || idx == len(combined)-1 {
		return "", ""
	}
	return combined[:idx], combined[idx+1:]
}
