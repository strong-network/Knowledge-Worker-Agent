// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeserver"
)

// serverSupervisor is the process-wide pool of `opencode serve` instances,
// created lazily on first use in server mode.
var (
	serverSupervisor   *opencodeserver.Supervisor
	serverSupervisorMu sync.Mutex
)

// Supervisor returns the shared opencode-serve supervisor, creating it on first
// call. Returns nil if the opencode binary can't be ensured.
func Supervisor() *opencodeserver.Supervisor {
	serverSupervisorMu.Lock()
	defer serverSupervisorMu.Unlock()
	if serverSupervisor != nil {
		return serverSupervisor
	}
	if err := ensureOpencodeAvailable(); err != nil {
		log.Printf("[opencode-serve] cannot ensure opencode binary: %v", err)
		return nil
	}
	serverSupervisor = opencodeserver.NewSupervisor(opencodeserver.Options{
		Bin:      config.OpencodeBin,
		Hostname: "127.0.0.1",
		Env:      os.Environ(),
	})
	return serverSupervisor
}

// CloseSupervisor terminates all managed `opencode serve` processes if a
// supervisor was ever created. It does NOT lazily create one (unlike
// Supervisor), so it is safe to call unconditionally on shutdown. Idempotent.
func CloseSupervisor() {
	serverSupervisorMu.Lock()
	sup := serverSupervisor
	serverSupervisor = nil
	serverSupervisorMu.Unlock()
	if sup != nil {
		sup.Close()
	}
}

// ReloadServerAgents flags the running `opencode serve` instances (if any) so
// the next chat turn spins up a fresh server that re-reads the on-disk agent
// files. This lets newly created/edited/deleted agents take effect immediately
// instead of only after a full restart. It does NOT lazily create a supervisor,
// so it is a no-op in CLI-backend mode or before the pool is used. Running turns
// are not interrupted (the swap happens on the next Ensure).
func ReloadServerAgents() {
	serverSupervisorMu.Lock()
	sup := serverSupervisor
	serverSupervisorMu.Unlock()
	if sup != nil {
		sup.Reload()
	}
}

// buildServerTurnRequest assembles the opencode-serve request for one turn.
// Split out from runOpencodeServerStream so the prompt this backend actually
// sends — preamble and any forced skill included — can be asserted without
// standing up a real `opencode serve` process. The server path is the one with
// interactive permission approval and the likelier future default, so a skill
// that silently failed to be forced here would be the harder bug to notice.
func buildServerTurnRequest(sessionID, prompt string, cfg config.SessionConfig, forced []config.ForcedSkill, author Author) opencodeserver.TurnRequest {
	providerID, modelID := opencodeserver.PrepareModel(config.ResolveModel(cfg.Model))
	return opencodeserver.TurnRequest{
		AppSessionID:    sessionID,
		OpencodeSession: db.GetOpencodeSession(sessionID),
		Workdir:         cfg.Workdir,
		FallbackDir:     config.Workspace,
		Prompt:          config.PromptWithSessionPreambleAs(prompt, cfg, author.guestName(), forced...),
		Title:           cfg.Label,
		ProviderID:      providerID,
		ModelID:         modelID,
		Agent:           serverAgent(cfg),
		Variant:         cfg.ReasoningEffort,
	}
}

// runOpencodeServerStream drives a chat turn against the long-lived
// `opencode serve` backend, translating neutral opencodeserver.Events into the
// internal chat.Event stream consumed by the dispatcher.
func runOpencodeServerStream(sessionID, prompt string, cfg config.SessionConfig, forced []config.ForcedSkill, author Author) (<-chan *Event, *ActiveProcess) {
	ch := make(chan *Event, 64)

	sup := Supervisor()
	if sup == nil {
		ch <- &Event{Kind: "error", Text: "opencode server backend is not available"}
		close(ch)
		return ch, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	req := buildServerTurnRequest(sessionID, prompt, cfg, forced, author)

	result, err := sup.RunTurn(ctx, req)
	if err != nil {
		cancel()
		ch <- &Event{Kind: "error", Text: fmt.Sprintf("opencode server backend: %v", err)}
		close(ch)
		return ch, nil
	}

	// Replace any prior process for this session.
	if val, ok := Procs.LoadAndDelete(sessionID); ok {
		val.(*ActiveProcess).Kill()
	}
	opencodeSession := result.SessionID()
	turnClient := result.Client()
	proc := &ActiveProcess{
		Abort: func() {
			// Abort the remote session using the turn's own client, then cancel
			// the SSE subscription. We deliberately do NOT re-Ensure a server
			// here: if the instance is gone, there's nothing to abort, and
			// spinning up a fresh process during teardown would be wasteful.
			if turnClient != nil && opencodeSession != "" {
				actx, acancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = turnClient.Abort(actx, opencodeSession)
				acancel()
			}
			cancel()
		},
	}
	Procs.Store(sessionID, proc)

	go func() {
		defer func() {
			Procs.Delete(sessionID)
			cancel()
			close(ch)
			log.Printf("[opencode-serve] session=%s stream ended", sessionID)
		}()

		seenTools := map[string]bool{}
		calledTools := map[string]bool{}
		for e := range result.Events {
			translateServerEvent(e, ch, seenTools, calledTools)
		}
		// Finalize the collapsed todo row (if any).
		if seenTools[opencodeTodoCallID] {
			ch <- &Event{Kind: "tool_done", CallID: opencodeTodoCallID, Tool: "todowrite", Success: true}
		}
	}()

	return ch, proc
}

// translateServerEvent converts one neutral opencodeserver.Event into the
// internal chat.Event(s) and pushes them onto ch. It reproduces the CLI path's
// UX behaviors: synthesizing a tool_call before tool_done, and collapsing the
// todo tool into one live checklist under a stable id.
//
// seenTools tracks call_ids whose terminal tool_done has been emitted (de-dupes
// opencode's re-sent completed parts). calledTools tracks call_ids whose opening
// tool_call has already been emitted (a `task` delegation emits its tool_call
// early, on `running`), so the terminal tool_done reconciles that same row
// instead of appending a duplicate.
func translateServerEvent(e opencodeserver.Event, ch chan<- *Event, seenTools, calledTools map[string]bool) {
	switch e.Kind {
	case opencodeserver.KindChunk:
		ch <- &Event{Kind: "chunk", Text: e.Text, SessionID: e.SessionID}

	case opencodeserver.KindStep:
		// Tool carries an optional subagent-progress label (the child tool's
		// name) so a running `task` row can show live progress.
		ch <- &Event{Kind: "step", SessionID: e.SessionID, Tool: e.Tool}

	case opencodeserver.KindToolCall:
		// A tool entered `running` (emitted for the `task` delegation): show a
		// live in-flight row now. Skip the todo tool (collapsed separately on
		// completion) and de-dupe repeated running updates by call_id.
		if isOpencodeTodoTool(e.Tool) {
			return
		}
		if e.CallID == "" || calledTools[e.CallID] {
			return
		}
		calledTools[e.CallID] = true
		ch <- &Event{Kind: "tool_call", CallID: e.CallID, Tool: e.Tool, Args: e.Args, SessionID: e.SessionID}

	case opencodeserver.KindToolDone:
		// Collapse the todo tool into a single live checklist.
		if isOpencodeTodoTool(e.Tool) {
			seenTools[opencodeTodoCallID] = true
			ch <- &Event{Kind: "tool_call", CallID: opencodeTodoCallID, Tool: e.Tool, Args: e.Args}
			return
		}
		// opencode may re-send the completed tool part; emit exactly one
		// tool_done per callID.
		if e.CallID != "" && seenTools[e.CallID] {
			return
		}
		if e.CallID != "" {
			seenTools[e.CallID] = true
		}
		// Emit the opening tool_call only if a live (running) one wasn't already
		// sent for this call_id — otherwise reconcile the existing row.
		if e.CallID == "" || !calledTools[e.CallID] {
			ch <- &Event{Kind: "tool_call", CallID: e.CallID, Tool: e.Tool, Args: e.Args}
			if e.CallID != "" {
				calledTools[e.CallID] = true
			}
		}
		ch <- &Event{Kind: "tool_done", CallID: e.CallID, Tool: e.Tool, Success: e.Success, Output: e.Output, SessionID: e.SessionID}

	case opencodeserver.KindToolBlocked:
		if e.CallID != "" && !calledTools[e.CallID] {
			calledTools[e.CallID] = true
			ch <- &Event{Kind: "tool_call", CallID: e.CallID, Tool: e.Tool, Args: e.Args}
		}
		ch <- &Event{Kind: "tool_blocked", CallID: e.CallID, Tool: e.Tool, Args: e.Args, Output: e.Output, SessionID: e.SessionID}

	case opencodeserver.KindPermission:
		ch <- &Event{
			Kind:         "permission",
			PermissionID: e.PermissionID,
			Permission:   e.Permission,
			Patterns:     e.Patterns,
			CallID:       e.CallID,
			SessionID:    e.SessionID,
		}

	case opencodeserver.KindError:
		ch <- &Event{Kind: "error", Text: e.Text, SessionID: e.SessionID}

	case opencodeserver.KindResult:
		ch <- &Event{
			Kind:          "result",
			SessionID:     e.SessionID,
			TokensIn:      e.TokensIn,
			TokensOut:     e.TokensOut,
			TokensInKnown: e.TokensInKnown,
			Cost:          e.Cost,
		}
	}
}

// serverAgent resolves the opencode agent for server mode from the session
// config, mirroring BuildOpencodeArgs: an explicit agent wins, otherwise
// "plan" mode maps to opencode's built-in read-only `plan` agent.
func serverAgent(cfg config.SessionConfig) string {
	if a := strings.TrimSpace(cfg.Agent); a != "" {
		return a
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Mode), "plan") {
		return "plan"
	}
	return ""
}
