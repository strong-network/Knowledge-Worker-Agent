// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// A Code Mode program that called two connectors, as opencode 2.2.5 printed it
// (the bulky provider fields and the long output are trimmed).
const codeModeTwoConnectors = `{"type":"tool_use","timestamp":1793448594351,"sessionID":"ses_cm","part":{"partID":"prt_1","sessionID":"ses_cm","messageID":"msg_1","type":"tool","id":"toolu_cm","tool":"execute","state":{"status":"completed","input":{"code":"const [a,b] = await Promise.all([\n tools[\"microsoft-learn\"].microsoft_docs_search({query:\"Azure Functions timeout\"}),\n tools.deepwiki.ask_wiki_question({repoName:\"facebook/react\",question:\"What is this repo about?\"})]);\nreturn {first:a.results?.[0]?.title, b:b.result};"},"output":"{\"first\":\"Azure Functions hosting options\"}","metadata":{"metadata":{"toolCalls":[{"tool":"microsoft-learn.microsoft_docs_search","status":"completed","input":{"query":"Azure Functions timeout"}},{"tool":"deepwiki.ask_wiki_question","status":"completed","input":{"repoName":"facebook/react","question":"What is this repo about?"}}],"truncated":false}},"time":{"start":1,"end":2}}}}`

func TestCodeModeCallsBecomeRows(t *testing.T) {
	ev := ParseOpencodeJSONL(codeModeTwoConnectors)
	if ev == nil || ev.Tool != "execute" {
		t.Fatalf("event = %+v", ev)
	}
	if len(ev.Calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(ev.Calls))
	}
	first, second := ev.Calls[0], ev.Calls[1]
	if first.Tool != "microsoft-learn_microsoft_docs_search" || second.Tool != "deepwiki_ask_wiki_question" {
		t.Errorf("tools = %q, %q; want the 1.x <server>_<tool> names", first.Tool, second.Tool)
	}
	if first.CallID != "toolu_cm/0" || second.CallID != "toolu_cm/1" {
		t.Errorf("call ids = %q, %q", first.CallID, second.CallID)
	}
	if first.Args["query"] != "Azure Functions timeout" {
		t.Errorf("args = %v", first.Args)
	}
	for _, c := range ev.Calls {
		if c.Kind != "tool_done" || !c.Success || c.SessionID != "ses_cm" {
			t.Errorf("row = %+v", c)
		}
		// The result belongs to the program, not to either call.
		if c.Output != "" {
			t.Errorf("row %s carries the program's output", c.Tool)
		}
	}
	// The rows attribute to their connectors the way 1.x's calls did.
	servers := []string{"deepwiki", "microsoft-learn"}
	for _, c := range ev.Calls {
		if server, _ := splitMcpTool(c.Tool, servers); server == "" {
			t.Errorf("%s is not attributed to a connector", c.Tool)
		}
	}
}

func codeModeLine(calls, output string, programFailed bool) string {
	errFlag := ""
	if programFailed {
		errFlag = `,"error":true`
	}
	return `{"type":"tool_use","sessionID":"ses_cm","part":{"id":"toolu_x","tool":"execute","state":{"status":"completed",` +
		`"input":{"code":"..."},"output":` + output + `,"metadata":{"metadata":{"toolCalls":[` + calls + `]` + errFlag + `}}}}}`
}

func TestCodeModeSingleCallCarriesOutput(t *testing.T) {
	ev := ParseOpencodeJSONL(codeModeLine(`{"tool":"opencode.list_mcp_resources","status":"completed"}`, `"{\"resources\":[]}"`, false))
	if len(ev.Calls) != 1 {
		t.Fatalf("calls = %d", len(ev.Calls))
	}
	row := ev.Calls[0]
	if row.Tool != "list_mcp_resources" {
		t.Errorf("tool = %q; opencode's own tools have no prefix", row.Tool)
	}
	if row.Output != `{"resources":[]}` {
		t.Errorf("output = %q", row.Output)
	}
}

func TestCodeModeFailedCalls(t *testing.T) {
	calls := `{"tool":"search","status":"completed","input":{"query":"jira"}},` +
		`{"tool":"atlassian.getJiraIssue","status":"error","input":{"issueIdOrKey":"PLAT-1"}}`
	ev := ParseOpencodeJSONL(codeModeLine(calls, `"ToolFailure: issue not found"`, true))
	if len(ev.Calls) != 2 {
		t.Fatalf("calls = %d", len(ev.Calls))
	}
	if ev.Calls[0].Tool != "search" || !ev.Calls[0].Success {
		t.Errorf("search row = %+v", ev.Calls[0])
	}
	failed := ev.Calls[1]
	if failed.Kind != "tool_done" || failed.Success || failed.Output != "ToolFailure: issue not found" {
		t.Errorf("failed row = %+v", failed)
	}

	denied := ParseOpencodeJSONL(codeModeLine(`{"tool":"atlassian.getJiraIssue","status":"error"}`,
		`"The user rejected permission to use this specific tool call."`, true))
	if denied.Calls[0].Kind != "tool_blocked" {
		t.Errorf("a refused call is %q, want tool_blocked", denied.Calls[0].Kind)
	}

	// A call still running when the program ended did not complete.
	cut := ParseOpencodeJSONL(codeModeLine(`{"tool":"deepwiki.ask_wiki_question","status":"running"}`, `"interrupted"`, true))
	if cut.Calls[0].Success {
		t.Error("a call that never finished is shown as a success")
	}
}

func TestCodeModeProgramWithoutCalls(t *testing.T) {
	ev := ParseOpencodeJSONL(codeModeLine("", `"2"`, false))
	if ev.Calls != nil || ev.Tool != "execute" || !ev.Success {
		t.Errorf("event = %+v; a program that calls nothing is one row", ev)
	}
	failed := ParseOpencodeJSONL(codeModeLine("", `"ReferenceError: Unknown identifier 'require'."`, true))
	if failed.Success {
		t.Error("a failed program is shown as a success")
	}
}

// A tool another server happens to name "execute" is not a program: only the
// bare 2.x tool is expanded.
func TestCodeModeOnlyForTheExecuteTool(t *testing.T) {
	line := strings.Replace(codeModeTwoConnectors, `"tool":"execute"`, `"tool":"shell_execute"`, 1)
	if ev := ParseOpencodeJSONL(line); ev.Calls != nil {
		t.Errorf("calls = %d for shell_execute", len(ev.Calls))
	}
}

func TestStripSystemReminders(t *testing.T) {
	// A reply Haiku gave on the first 2.x turn of a conversation started on 1.x.
	copied := "<system-reminder>\nThe Code Mode tool catalog has been updated. I can now use `execute` to call tools.\n</system-reminder>\n\n**note.txt contains:**\n```\nhello-from-v1\n```"
	if got := stripSystemReminders(copied); got != "**note.txt contains:**\n```\nhello-from-v1\n```" {
		t.Errorf("stripped = %q", got)
	}
	plain := "No notes here, just <b>text</b>."
	if got := stripSystemReminders(plain); got != plain {
		t.Errorf("changed a plain reply: %q", got)
	}
	// An unclosed tag is not a copied block; leave it.
	open := "<system-reminder> is a tag opencode uses"
	if got := stripSystemReminders(open); got != open {
		t.Errorf("changed an unclosed tag: %q", got)
	}
	only := `{"type":"text","sessionID":"s","part":{"type":"text","text":"<system-reminder>catalog</system-reminder>\n"}}`
	if ev := ParseOpencodeJSONL(only); ev != nil {
		t.Errorf("a reply that was only a copied note produced %+v", ev)
	}
}

// The stream shows a program's calls as rows of their own, each with its
// tool_call, and never the program itself.
func TestRunStreamExpandsCodeMode(t *testing.T) {
	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "test.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	folder := filepath.Join(tmp, "chat")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(tmp, "out.jsonl")
	if err := os.WriteFile(out, []byte(codeModeTwoConnectors+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "opencode")
	script := "#!/bin/sh\n" +
		"case \"$1\" in --version) echo 'opencode v2.2.5'; exit 0;; serve) exit 1;; esac\n" +
		"cat " + out + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KWA_OPENCODE_AUTO_UPDATE", "false")
	t.Setenv("KWA_HOME", filepath.Join(tmp, "home"))
	t.Setenv("KWA_OPENCODE_INSTALL_DIR", filepath.Join(tmp, "install"))
	prevEnsured, prevBin, prevWS := opencodeEnsured, config.OpencodeBin, config.Workspace
	opencodeEnsured = false
	t.Cleanup(func() { opencodeEnsured, config.OpencodeBin, config.Workspace = prevEnsured, prevBin, prevWS })
	config.Workspace, config.OpencodeBin = tmp, bin

	cfg := config.SessionConfig{Backend: config.BackendOpencode, Workdir: folder, Model: "github-copilot/claude-sonnet-5.5", Yolo: true}
	if err := db.CreateSession("cm-test", cfg); err != nil {
		t.Fatal(err)
	}
	events, _ := RunStream("cm-test", "hello", cfg)
	var got []string
	timeout := time.After(30 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-events:
			if !ok {
				done = true
				break
			}
			if strings.HasPrefix(ev.Kind, "tool_") {
				got = append(got, ev.Kind+" "+ev.CallID+" "+ev.Tool)
			}
		case <-timeout:
			t.Fatal("timed out waiting for the fake opencode stream")
		}
	}
	want := []string{
		"tool_call toolu_cm/0 microsoft-learn_microsoft_docs_search",
		"tool_done toolu_cm/0 microsoft-learn_microsoft_docs_search",
		"tool_call toolu_cm/1 deepwiki_ask_wiki_question",
		"tool_done toolu_cm/1 deepwiki_ask_wiki_question",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
