// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessions

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workdirs"
)

// sideChat asks for a side chat of parent.
func sideChat(t *testing.T, parent string) (int, string, config.SessionConfig) {
	t.Helper()
	code, body := call(t, "POST", "/api/sessions/"+parent+"/side", "")
	var resp struct {
		SessionID string               `json:"session_id"`
		Config    config.SessionConfig `json:"config"`
	}
	json.Unmarshal([]byte(body), &resp)
	if resp.SessionID != "" {
		t.Cleanup(func() { chat.Streams.Delete(resp.SessionID); chat.ClearQueue(resp.SessionID) })
	}
	return code, resp.SessionID, resp.Config
}

// A side chat is a temporary chat in its parent's folder, set to fork the
// parent's conversation, with its own name and none of the parent's starring.
func TestSideChatWorksInItsParentsFolder(t *testing.T) {
	temporaryChatTest(t)
	parent, folder := newFolderChat(t)
	call(t, "POST", "/api/sessions/"+parent+"/favorite", "")

	code, sid, cfg := sideChat(t, parent)
	if code != http.StatusOK || sid == "" || sid == parent {
		t.Fatalf("side chat = %d %q", code, sid)
	}
	if cfg.Workdir != folder || cfg.ParentID != parent || !cfg.Fork || !cfg.Temporary {
		t.Errorf("side chat config = workdir %q parent %q fork %v temporary %v, want %q %q true true",
			cfg.Workdir, cfg.ParentID, cfg.Fork, cfg.Temporary, folder, parent)
	}
	if cfg.Favorite || cfg.Pinned || cfg.Keep || cfg.Label != "Side chat" || cfg.LabelManual {
		t.Errorf("side chat should be unstarred and named by its first message: %+v", cfg)
	}
	stored, _ := db.GetSessionConfig(sid)
	if stored.ParentID != parent || !stored.Fork {
		t.Errorf("stored side chat = parent %q fork %v", stored.ParentID, stored.Fork)
	}

	_, list := call(t, "GET", "/api/sessions", "")
	var rows []db.SessionListItem
	json.Unmarshal([]byte(list), &rows)
	found := false
	for _, r := range rows {
		if r.SessionID == sid {
			found = true
			if r.ParentID != parent || !r.Temporary {
				t.Errorf("listed side chat = parent %q temporary %v", r.ParentID, r.Temporary)
			}
		}
	}
	if !found {
		t.Errorf("side chat missing from the list: %s", list)
	}
}

// Nobody can make a chat a side chat, or stop one being one, by sending the
// fields: on a new chat or in a settings change.
func TestSideChatFieldsAreTheServers(t *testing.T) {
	temporaryChatTest(t)
	parent, _ := newFolderChat(t)

	code, body := call(t, "POST", "/api/sessions/new", `{"label":"x","parent_id":"`+parent+`","fork":true}`)
	if code != http.StatusOK {
		t.Fatalf("new chat = %d %s", code, body)
	}
	var made struct {
		SessionID string `json:"session_id"`
	}
	json.Unmarshal([]byte(body), &made)
	if cfg, _ := db.GetSessionConfig(made.SessionID); cfg.ParentID != "" || cfg.Fork {
		t.Errorf("a new chat took parent %q fork %v from the client", cfg.ParentID, cfg.Fork)
	}

	_, sid, _ := sideChat(t, parent)
	code, body = call(t, "PATCH", "/api/sessions/"+sid+"/config", `{"parent_id":"","fork":false,"model":"m"}`)
	if code != http.StatusOK {
		t.Fatalf("config update = %d %s", code, body)
	}
	if cfg, _ := db.GetSessionConfig(sid); cfg.ParentID != parent || !cfg.Fork {
		t.Errorf("a config update changed parent to %q, fork to %v", cfg.ParentID, cfg.Fork)
	}
}

// A chat that's gone or read-only can't have a side chat.
func TestSideChatNeedsAWorkingParent(t *testing.T) {
	temporaryChatTest(t)
	if code, _, _ := sideChat(t, "missing"); code != http.StatusNotFound {
		t.Errorf("side chat of a missing chat = %d, want 404", code)
	}
	// CreateSession would update the backend, so store it as an old chat has it.
	if _, err := db.DB.Exec(`INSERT INTO sessions (id, config) VALUES ('old', '{"backend":"copilot"}')`); err != nil {
		t.Fatal(err)
	}
	if !db.IsSessionDeprecated("old") {
		t.Fatal("test chat should be read-only")
	}
	if code, _, _ := sideChat(t, "old"); code != http.StatusConflict {
		t.Errorf("side chat of a read-only chat = %d, want 409", code)
	}
}

// Deleting either chat leaves the folder the other works in; deleting the
// last one removes it.
func TestFolderStaysWhileASideChatUsesIt(t *testing.T) {
	temporaryChatTest(t)
	workdirs.InUse = db.FolderInUse
	t.Cleanup(func() { workdirs.InUse = func(string) bool { return false } })

	parent, folder := newFolderChat(t)
	_, side, _ := sideChat(t, parent)
	if code, body := call(t, "DELETE", "/api/sessions/"+parent, ""); code != http.StatusOK {
		t.Fatalf("delete parent = %d %s", code, body)
	}
	if _, err := os.Stat(folder); err != nil {
		t.Fatalf("deleting the parent removed the folder its side chat works in: %v", err)
	}
	if code, body := call(t, "DELETE", "/api/sessions/"+side, ""); code != http.StatusOK {
		t.Fatalf("delete side chat = %d %s", code, body)
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Errorf("the folder should go with the last chat in it, err = %v", err)
	}

	parent, folder = newFolderChat(t)
	_, side, _ = sideChat(t, parent)
	call(t, "DELETE", "/api/sessions/"+side, "")
	if _, err := os.Stat(folder); err != nil {
		t.Fatalf("deleting a side chat removed its parent's folder: %v", err)
	}
}
