// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeinstaller"
)

// ActiveProcess tracks a running chat backend process.
//
// In CLI mode it wraps the spawned `opencode run` process (Cmd). In
// server mode there is no local child process; instead Abort holds a function
// that aborts the opencode session over HTTP, and Cancel stops the SSE
// subscription. Kill() invokes whichever mechanism is present.
type ActiveProcess struct {
	Cmd   *exec.Cmd
	Abort func() // server mode: abort the remote session + cancel the stream
	mu    sync.Mutex
}

func (p *ActiveProcess) Kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Abort != nil {
		p.Abort()
	}
	if p.Cmd != nil && p.Cmd.Process != nil {
		p.Cmd.Process.Kill()
	}
}

// Procs stores active processes keyed by session ID.
var Procs sync.Map

// StopSession stops any in-flight turn for a session: it kills the running
// backend process (which, for the opencode-serve backend, aborts the remote
// session) and finishes the SSE stream so connected clients see the turn end.
// Safe to call when nothing is running. Used by session/project deletion.
// Reports whether a process was running.
func StopSession(sessionID string) (stopped bool) {
	// Finish first: the dispatcher treats an error arriving on a finished
	// stream as the stop, not as a failure of the turn.
	if val, ok := Streams.Load(sessionID); ok {
		val.(*Stream).Finish()
	}
	if val, ok := Procs.LoadAndDelete(sessionID); ok {
		val.(*ActiveProcess).Kill()
		stopped = true
	}
	ClearQueue(sessionID)
	return stopped
}

// DeleteOpencodeSession removes the opencode-side session that backed a chat.
//
// opencode keeps its own store, and it keeps everything: the transcript lives
// there as an append-only event log with no retention of any kind, and the
// only thing that reclaims it is `opencode session delete`, which cascades to
// that session's events, parts and messages. Deleting our row alone left the
// transcript on disk permanently — invisible in the UI and unreachable by
// tidy-up, which is the same trap workspace hygiene fixed for chat folders. The user asked
// for the chat to be gone, so it goes from both stores.
//
// The id must be read before our row is deleted, because it is stored on that
// row. An empty id is a no-op: a copilot-backend chat, or one that never ran a
// turn, has no opencode session to remove.
//
// The work runs in the background and never fails the deletion. The chat is
// already gone from the UI by then, and a surviving opencode session is a
// disk-space problem, not a correctness one.
func DeleteOpencodeSession(opencodeSessionID string) {
	id := strings.TrimSpace(opencodeSessionID)
	if id == "" {
		return
	}
	go deleteOpencodeSession(config.OpencodeBin, id)
}

// deleteOpencodeSession runs the CLI delete. Kept synchronous and separate so
// tests can assert on it without racing the goroutine above.
func deleteOpencodeSession(bin, opencodeSessionID string) {
	if bin == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "session", "delete", opencodeSessionID).CombinedOutput()
	if err == nil {
		return
	}
	// A session opencode has already forgotten is the expected outcome of a
	// retried delete, not a failure worth reporting. Logging it would train
	// the reader to ignore this line, which is where real failures hide.
	if strings.Contains(string(out), "Session not found") {
		return
	}
	log.Printf("[sessions] could not delete opencode session %s: %v: %s",
		opencodeSessionID, err, strings.TrimSpace(string(out)))
}

var (
	opencodeInstallMu sync.Mutex
	opencodeEnsured   bool
)

// Event represents a parsed chat-backend event normalized for the SSE layer.
type Event struct {
	Kind      string         `json:"kind"`
	Text      string         `json:"text,omitempty"`
	Tool      string         `json:"tool,omitempty"`
	CallID    string         `json:"call_id,omitempty"`
	Args      map[string]any `json:"args,omitempty"`
	Success   bool           `json:"success,omitempty"`
	Output    string         `json:"output,omitempty"`
	Question  string         `json:"question,omitempty"`
	Choices   []string       `json:"choices,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	McpServer string         `json:"mcp_server,omitempty"`
	// Permission fields (Kind == "permission"): an interactive permission
	// request from the opencode server backend that the user can allow/deny.
	PermissionID  string   `json:"permission_id,omitempty"`
	Permission    string   `json:"permission,omitempty"`
	Patterns      []string `json:"patterns,omitempty"`
	TokensIn      int      `json:"tokens_in,omitempty"`
	TokensInKnown bool     `json:"tokens_in_known,omitempty"`
	TokensOut     int      `json:"tokens_out,omitempty"`
	Cost          float64  `json:"cost,omitempty"`
	PremiumReqs   int      `json:"premium_reqs,omitempty"`
	FilesModified int      `json:"files_modified,omitempty"`
	LinesAdded    int      `json:"lines_added,omitempty"`
	LinesRemoved  int      `json:"lines_removed,omitempty"`

	// Synthetic marks an event the server produced itself rather than one read
	// back from the model — currently the tool pair standing in for a skill the
	// user armed in the composer. It never reaches the browser; the dispatcher
	// uses it to keep counts that mean "work the model did" honest.
	Synthetic bool `json:"-"`
}

// ParseOpencodeJSONL parses a single JSONL line from opencode run --format json.
func ParseOpencodeJSONL(line string) *Event {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		return nil
	}
	t, _ := obj["type"].(string)
	part, _ := obj["part"].(map[string]any)
	if part == nil {
		part = map[string]any{}
	}

	switch t {
	case "step_start":
		// opencode emits step_start ~1-3s after the message lands — well
		// before the final assistant text. We forward it as a lightweight
		// "step" event so the UI can show that the agent is actively working
		// (instead of just an indefinite spinner). The github-copilot
		// provider in opencode does NOT emit incremental text deltas; the
		// `text` event below carries the complete response in a single
		// chunk. Surfacing step_start is the best feedback we have until
		// upstream adds text streaming.
		return &Event{Kind: "step", SessionID: strOr(obj, "sessionID", "sessionId", "session_id")}

	case "step_finish":
		// step_finish carries the token & cost accounting for one model turn:
		//   part.tokens = {input, output, reasoning, cache: {read, write}}
		//   part.cost   = float (USD)
		// For prompt-caching models (e.g. Claude via Copilot) the bulk of the
		// re-fed context — file contents, prior turns — is billed as cache
		// read/write tokens, NOT as plain "input". Counting only `input` here
		// wildly under-reports input (e.g. 2 input tokens for a multi-dollar
		// turn), so include cache read+write in the input total.
		tokens, _ := part["tokens"].(map[string]any)
		cache, _ := tokens["cache"].(map[string]any)
		ev := &Event{
			Kind:          "step_finish",
			SessionID:     strOr(obj, "sessionID", "sessionId", "session_id"),
			TokensIn:      toInt(tokens["input"]) + toInt(cache["read"]) + toInt(cache["write"]),
			TokensOut:     toInt(tokens["output"]) + toInt(tokens["reasoning"]),
			TokensInKnown: tokens != nil,
			Cost:          toFloat(part["cost"]),
		}
		return ev

	case "text":
		text := extractContentText(part["text"])
		if text == "" {
			return nil
		}
		return &Event{Kind: "chunk", Text: text, SessionID: strOr(obj, "sessionID", "sessionId", "session_id")}

	case "tool_use":
		tool := strOr(part, "tool", "name")
		callID := strOr(part, "id", "callID", "callId")
		state, _ := part["state"].(map[string]any)
		status := strOr(state, "status")
		success := status != "error"
		output := extractOutput(state)
		if output == "" {
			output = extractOutput(part)
		}
		if output == "" && state["error"] != nil {
			output = fmt.Sprint(state["error"])
		}
		if len(output) > 2000 {
			output = output[:2000]
		}
		args, _ := part["input"].(map[string]any)
		if args == nil {
			// opencode nests the tool input under state.input for rejected
			// calls (the top-level part.input may be absent).
			args, _ = state["input"].(map[string]any)
		}
		sid := strOr(obj, "sessionID", "sessionId", "session_id")
		// A permission "ask" in headless `opencode run` is auto-rejected:
		// the tool comes back as an error with a "rejected permission"
		// message. Surface this as a distinct tool_blocked event so the UI
		// can show a clear "needs approval / denied" card instead of a
		// generic tool error.
		if status == "error" && isPermissionDenied(output) {
			return &Event{Kind: "tool_blocked", CallID: callID, Tool: tool, Args: args, Output: output, SessionID: sid}
		}
		return &Event{Kind: "tool_done", CallID: callID, Tool: tool, Args: args, Success: success, Output: output, SessionID: sid}

	case "error":
		msg := extractOpencodeError(obj["error"])
		if msg == "" {
			msg = "opencode returned an error event"
		}
		return &Event{Kind: "error", Text: msg, SessionID: strOr(obj, "sessionID", "sessionId", "session_id")}
	}
	return nil
}

// isPermissionDenied reports whether a tool error message indicates the call
// was blocked by opencode's permission system (an "ask" rule auto-rejected in
// headless `opencode run`, or an explicit "deny" rule). Matches the wording
// opencode emits, e.g. "The user rejected permission to use this specific tool
// call." and "permission denied".
func isPermissionDenied(msg string) bool {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "rejected permission"):
		return true
	case strings.Contains(low, "permission denied"):
		return true
	case strings.Contains(low, "permission to use") && strings.Contains(low, "reject"):
		return true
	default:
		return false
	}
}

// opencodeTodoCallID is a synthetic, stable tool-call id used for opencode's
// todo tool. opencode emits the todo list as a fresh tool_use event (a new
// part.id and callID) on every update — pending → in_progress → completed.
// Collapsing them under one id makes the UI render a single checklist that
// updates in place (like copilot's TodoWrite) instead of N separate cards.
const opencodeTodoCallID = "opencode-todo"

// isOpencodeTodoTool reports whether an opencode tool name is the todo tool.
// opencode names it "todowrite"/"todoread" (lowercase); accept the underscore
// variants defensively to match the frontend's todo detection.
func isOpencodeTodoTool(name string) bool { return IsTodoTool(name) }

// IsTodoTool reports whether a tool is the plan tool, whose arguments are the
// plan itself (guests of a shared chat see them).
func IsTodoTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "todowrite", "todo_write", "todoread", "todo_read":
		return true
	default:
		return false
	}
}

// RunStream starts the opencode chat backend for the session and streams
// parsed events to the channel. OpenCode is the only supported backend.
//
// Two execution paths exist:
//   - server mode (KWA_OPENCODE_USE_SERVER truthy): drives a long-lived
//     `opencode serve` process over HTTP/SSE (see internal/opencodeserver).
//   - CLI mode (default): spawns `opencode run --format json` per turn and
//     scrapes stdout JSONL.
//
// turn carries settings that belong to this one prompt and are never persisted
// (a skill forced from the composer). It is variadic so callers that force nothing keep
// their existing call shape.
func RunStream(sessionID, prompt string, cfg config.SessionConfig, turn ...Turn) (events <-chan *Event, proc *ActiveProcess) {
	activeTurns.Add(1)
	defer func() { events = finishTrackedTurn(events) }()
	// Resolve the armed skill once, here, so both backends receive the same
	// already-resolved block and neither can be forgotten.
	var forced []config.ForcedSkill
	if t := firstTurn(turn); t.Skill != "" {
		if skill, ok := resolveForcedSkill(context.Background(), cfg.Workdir, t.Skill); ok {
			forced = append(forced, skill)
		}
	}
	author := firstTurn(turn).Author
	if useServerBackend() {
		events, proc := runOpencodeServerStream(sessionID, prompt, cfg, forced, author)
		return prependEvents(events, forcedSkillEvents(forced)), proc
	}
	events, proc = runOpencodeStream(sessionID, prompt, cfg, forced, author)
	return prependEvents(events, forcedSkillEvents(forced)), proc
}

// prependEvents returns a channel that yields head before relaying src. Used to
// put the server's own events (a forced skill) ahead of the model's, so the
// transcript reads in the order things actually applied. Returns src untouched
// when there is nothing to prepend, keeping the common path free of an extra
// goroutine and channel hop.
func prependEvents(src <-chan *Event, head []*Event) <-chan *Event {
	if len(head) == 0 {
		return src
	}
	out := make(chan *Event, len(head)+64)
	go func() {
		defer close(out)
		for _, e := range head {
			out <- e
		}
		for e := range src {
			out <- e
		}
	}()
	return out
}

// useServerBackend reports whether the HTTP server backend is enabled via
// KWA_OPENCODE_USE_SERVER (1/true/yes/on). Defaults to false (CLI path).
func useServerBackend() bool {
	switch strings.ToLower(strings.TrimSpace(env.Get("KWA_OPENCODE_USE_SERVER"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func runOpencodeStream(sessionID, prompt string, cfg config.SessionConfig, forced []config.ForcedSkill, author Author) (<-chan *Event, *ActiveProcess) {
	ch := make(chan *Event, 64)
	if err := ensureOpencodeAvailable(); err != nil {
		ch <- &Event{Kind: "error", Text: fmt.Sprintf("opencode backend is not available: %v", err)}
		close(ch)
		return ch, nil
	}

	opencodeSession := db.GetOpencodeSession(sessionID)
	prompt = config.PromptWithSessionPreambleAs(prompt, cfg, author.guestName(), forced...)
	args := config.BuildOpencodeArgs(prompt, opencodeSession, cfg)

	cwd := cfg.Workdir
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		cwd = config.Workspace
	}

	log.Printf("[DEBUG] session=%s opencode_resume=%q cwd=%s cmd=%s %s",
		sessionID, opencodeSession, cwd, config.OpencodeBin, argsForLog(args))

	cmd := exec.Command(config.OpencodeBin, args...)
	cmd.Dir = cwd
	cmd.Env = os.Environ()

	// Opencode's `run` reads stdin to detect piped input and BLOCKS until
	// it sees EOF. If we attach a pipe whose writer we keep open, opencode
	// never produces output — the process appears to hang and we time out with
	// "no output within 90s". opencode doesn't surface interactive questions over
	// stdin in `run --format json` mode anyway (permission asks come back as
	// auto-rejected tool_use error events, which we already handle as
	// tool_blocked), so we attach /dev/null and skip the pipe.
	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		ch <- &Event{Kind: "error", Text: fmt.Sprintf("opencode backend: cannot open /dev/null: %v", err)}
		close(ch)
		return ch, nil
	}
	cmd.Stdin = devNull

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		devNull.Close()
		ch <- &Event{Kind: "error", Text: err.Error()}
		close(ch)
		return ch, nil
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		devNull.Close()
		ch <- &Event{Kind: "error", Text: fmt.Sprintf("opencode backend is not available at %s: %v", config.OpencodeBin, err)}
		close(ch)
		return ch, nil
	}
	// The child has inherited the fd; we can close our copy.
	devNull.Close()

	if val, ok := Procs.LoadAndDelete(sessionID); ok {
		old := val.(*ActiveProcess)
		old.Kill()
	}

	proc := &ActiveProcess{Cmd: cmd}
	Procs.Store(sessionID, proc)

	go func() {
		defer func() {
			Procs.Delete(sessionID)
			cmd.Process.Kill()
			cmd.Wait()
			close(ch)
			log.Printf("[DEBUG] session=%s opencode stream ended, process cleaned up", sessionID)
		}()

		// Read opencode's JSONL output line-by-line with a bufio.Reader
		// (not bufio.Scanner): a single JSONL line can carry a large tool
		// result, file content, or diff snapshot that exceeds Scanner's
		// max-token limit ("token too long"). ReadString grows without a
		// hard per-line cap. A large initial buffer keeps small lines cheap.
		reader := bufio.NewReaderSize(stdout, 1024*1024)
		lineNum := 0
		sawAssistantOutput := false
		sawToolEvent := false
		sawError := false
		lastSessionID := opencodeSession

		gotOutput := make(chan struct{}, 1)
		startupTimeout := opencodeStartupTimeout()
		go func() {
			select {
			case <-gotOutput:
				return
			case <-time.After(startupTimeout):
				stderr := strings.TrimSpace(stderrBuf.String())
				if stderr == "" {
					stderr = fmt.Sprintf("opencode produced no output within %s. If this is the first run, make sure you have authenticated with `opencode auth login` (or set KWA_OPENCODE_STARTUP_TIMEOUT to a higher value).", startupTimeout)
				}
				log.Printf("[ERROR] session=%s opencode startup timeout, stderr=%s", sessionID, truncate(stderr, 1000))
				ch <- &Event{Kind: "error", Text: stderr}
				cmd.Process.Kill()
			}
		}()

		seenTools := map[string]bool{}
		// Accumulate per-step token/cost reports from opencode's
		// step_finish events. opencode reports a fresh count per step
		// (each model turn within a tool-using conversation is its own
		// step), so we sum them to get the turn total. tokensInKnown is
		// set as soon as we see any step_finish carrying a tokens object.
		tokensInAccum := 0
		tokensOutAccum := 0
		tokensInKnown := false
		costAccum := 0.0
		// processOpencodeLine parses one JSONL line and fans the resulting
		// event(s) onto ch, updating the turn-local accumulators/flags. Kept
		// as a closure so the read loop below stays focused on IO + EOF.
		processOpencodeLine := func(raw string) {
			event := ParseOpencodeJSONL(raw)
			if event == nil {
				log.Printf("[JSONL] session=%s backend=opencode line=%d bytes=%d (no event parsed)", sessionID, lineNum, len(raw))
				return
			}
			log.Printf("[JSONL] session=%s backend=opencode line=%d bytes=%d kind=%s", sessionID, lineNum, len(raw), event.Kind)
			if event.SessionID != "" {
				lastSessionID = event.SessionID
			}
			if event.Kind == "chunk" && strings.TrimSpace(event.Text) != "" {
				sawAssistantOutput = true
			}
			if event.Kind == "error" {
				sawError = true
			}
			if event.Kind == "tool_done" {
				sawToolEvent = true
				// opencode's todo tool: render as ONE live checklist that
				// updates in place. Each opencode todo update is a separate
				// tool_use (fresh callID) carrying the full todo list, so we
				// re-emit a tool_call under a stable id every time and skip
				// the per-update tool_done (which would flip the row to
				// "done" and carries no args). The frontend updates the
				// existing row when it sees the same callID again.
				if isOpencodeTodoTool(event.Tool) {
					seenTools[opencodeTodoCallID] = true
					ch <- &Event{Kind: "tool_call", CallID: opencodeTodoCallID, Tool: event.Tool, Args: event.Args}
					return
				}
				if event.CallID != "" && !seenTools[event.CallID] {
					seenTools[event.CallID] = true
					ch <- &Event{Kind: "tool_call", CallID: event.CallID, Tool: event.Tool, Args: event.Args}
				}
			}
			if event.Kind == "tool_blocked" {
				// Count as a tool event so the run isn't treated as "no
				// output". Emit a tool_call first (if unseen) so a tool row
				// exists for the UI to flip into the blocked state.
				sawToolEvent = true
				if event.CallID != "" && !seenTools[event.CallID] {
					seenTools[event.CallID] = true
					ch <- &Event{Kind: "tool_call", CallID: event.CallID, Tool: event.Tool, Args: event.Args}
				}
			}
			if event.Kind == "step_finish" {
				// Accumulate token totals; don't forward downstream — the
				// dispatcher only knows about a terminal "result" event for
				// usage stats, and we'll synthesize that below.
				if event.TokensInKnown {
					tokensInKnown = true
				}
				tokensInAccum += event.TokensIn
				tokensOutAccum += event.TokensOut
				costAccum += event.Cost
				return
			}
			ch <- event
		}
		for {
			line, readErr := reader.ReadString('\n')
			// Process any data returned even when readErr is io.EOF (the
			// final line may not be newline-terminated).
			raw := strings.TrimRight(line, "\r\n")
			if raw != "" {
				lineNum++
				if lineNum == 1 {
					select {
					case gotOutput <- struct{}{}:
					default:
					}
				}
				processOpencodeLine(raw)
			}
			if readErr != nil {
				if readErr != io.EOF {
					log.Printf("[ERROR] session=%s opencode read error: %v", sessionID, readErr)
				}
				break
			}
		}
		stderr := strings.TrimSpace(stderrBuf.String())
		if lineNum == 0 {
			select {
			case gotOutput <- struct{}{}:
			default:
			}
			if stderr == "" {
				stderr = "opencode process exited without producing any output"
			}
			log.Printf("[ERROR] session=%s opencode process exited with no output, stderr=%s", sessionID, truncate(stderr, 1000))
			ch <- &Event{Kind: "error", Text: stderr}
			return
		}
		if !sawAssistantOutput && !sawToolEvent && stderr != "" {
			log.Printf("[ERROR] session=%s opencode ended without response, stderr=%s", sessionID, truncate(stderr, 1000))
			ch <- &Event{Kind: "error", Text: stderr}
			return
		}
		if sawError && !sawAssistantOutput && !sawToolEvent {
			return
		}
		// Finalize the collapsed todo row (if any) so it stops showing a
		// running spinner once the turn completes. Args are omitted — the
		// frontend keeps the last todo list it received on tool_call.
		if seenTools[opencodeTodoCallID] {
			ch <- &Event{Kind: "tool_done", CallID: opencodeTodoCallID, Tool: "todowrite", Success: true}
		}
		ch <- &Event{
			Kind:          "result",
			SessionID:     lastSessionID,
			TokensIn:      tokensInAccum,
			TokensOut:     tokensOutAccum,
			TokensInKnown: tokensInKnown,
			Cost:          costAccum,
		}
	}()

	return ch, proc
}

func ensureOpencodeAvailable() error {
	opencodeInstallMu.Lock()
	defer opencodeInstallMu.Unlock()
	if opencodeEnsured && opencodeinstaller.VerifyBin(config.OpencodeBin) {
		return nil
	}

	autoUpdate := strings.ToLower(strings.TrimSpace(env.Get("KWA_OPENCODE_AUTO_UPDATE")))
	if autoUpdate == "" {
		autoUpdate = "true"
	}
	skipUpdate := autoUpdate == "0" || autoUpdate == "false" || autoUpdate == "no" || autoUpdate == "off"
	installDir := config.EnvOrDefault("KWA_OPENCODE_INSTALL_DIR", opencodeinstaller.DefaultInstallDir)

	if skipUpdate {
		if opencodeinstaller.VerifyBin(config.OpencodeBin) {
			opencodeEnsured = true
			return nil
		}
		installed, err := opencodeinstaller.Install(os.Stderr, opencodeinstaller.InstallOptions{InstallDir: installDir})
		if err != nil {
			return fmt.Errorf("auto-install failed: %w", err)
		}
		config.OpencodeBin = installed
		opencodeEnsured = true
		return nil
	}

	res, err := opencodeinstaller.Ensure(os.Stderr, opencodeinstaller.EnsureOptions{
		CurrentBin: config.OpencodeBin,
		InstallDir: installDir,
	})
	if err != nil {
		return fmt.Errorf("install/update failed: %w", err)
	}
	if res.Bin != "" {
		config.OpencodeBin = res.Bin
	}
	opencodeEnsured = true
	return nil
}

// NewUUID generates a UUID v4.
func NewUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func strOr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// toFloat coerces a JSON number (always float64 from encoding/json) to a
// float64. Returns 0 for anything else.
func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}

func extractOutput(data map[string]any) string {
	for _, key := range []string{"output", "result"} {
		if v, ok := data[key]; ok {
			switch o := v.(type) {
			case string:
				return o
			default:
				b, _ := json.Marshal(o)
				return string(b)
			}
		}
	}
	return ""
}

func extractOpencodeError(v any) string {
	switch errValue := v.(type) {
	case string:
		return errValue
	case map[string]any:
		// Prefer the structured human-readable message from the error
		// payload's data field over the bare class name, matching how
		// opencode's own CLI surfaces these errors.
		if data, ok := errValue["data"].(map[string]any); ok {
			if msg := strOr(data, "message", "error"); msg != "" {
				return msg
			}
		}
		if msg := strOr(errValue, "message", "name"); msg != "" {
			return msg
		}
		b, _ := json.Marshal(errValue)
		return string(b)
	default:
		return ""
	}
}

func extractContentText(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := m["text"].(string); ok && text != "" {
			parts = append(parts, text)
			continue
		}
		if content, ok := m["content"].(string); ok && content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "")
}

// argsForLog joins opencode's arguments for the log, with the prompt after
// "--" replaced by its length: the log outlives the chat it would quote.
func argsForLog(args []string) string {
	for i, a := range args {
		if a == "--" {
			n := 0
			for _, p := range args[i+1:] {
				n += len(p)
			}
			return strings.Join(append(args[:i:i], fmt.Sprintf("-- <prompt: %d bytes>", n)), " ")
		}
	}
	return strings.Join(args, " ")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// opencodeStartupTimeout returns the duration the runner waits for the
// opencode child process to emit its first stdout line before treating the
// run as stuck. Defaults to 90s (some providers — github-copilot in
// particular — perform OAuth token-exchange/device-flow on first invocation
// and routinely take >30s). Override via KWA_OPENCODE_STARTUP_TIMEOUT, parsed as
// either a bare seconds integer ("120") or a Go duration string ("90s",
// "2m"). Values <=0 disable the timeout (process exit is still surfaced).
func opencodeStartupTimeout() time.Duration {
	raw := strings.TrimSpace(env.Get("KWA_OPENCODE_STARTUP_TIMEOUT"))
	if raw == "" {
		return 90 * time.Second
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d <= 0 {
			return 24 * time.Hour // effectively disabled
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
