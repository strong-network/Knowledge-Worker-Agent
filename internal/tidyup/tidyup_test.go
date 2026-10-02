// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package tidyup

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

func setupDB(t *testing.T) {
	t.Helper()
	f, err := os.CreateTemp("", "tidyup-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() { db.Close(); os.Remove(path) })
	if err := db.Init(path); err != nil {
		t.Fatal(err)
	}
}

// enableReview turns the review surface on and restores the previous policy
// afterwards, so one test cannot leak settings into another.
func enableReview(t *testing.T, reviewAfter, snooze time.Duration) {
	t.Helper()
	prevEnabled := config.HygieneEnabled
	prevReview, prevSnooze := config.HygieneReviewAfter, config.HygieneSnooze
	t.Cleanup(func() {
		config.HygieneEnabled = prevEnabled
		config.HygieneReviewAfter, config.HygieneSnooze = prevReview, prevSnooze
	})
	config.HygieneEnabled = true
	config.HygieneReviewAfter, config.HygieneSnooze = reviewAfter, snooze
}

// idleChat creates a chat holding one message, last active idleFor ago.
func idleChat(t *testing.T, id, label, workdir string, idleFor time.Duration) {
	t.Helper()
	if err := db.CreateSession(id, config.SessionConfig{Label: label, Workdir: workdir}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddMessage(id, "user", "hello"); err != nil {
		t.Fatal(err)
	}
	// AddMessage counts as activity, so the idle clock is set afterwards.
	ts := time.Now().Add(-idleFor).UTC().Format(time.RFC3339)
	if _, err := db.DB.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, ts, id); err != nil {
		t.Fatal(err)
	}
}

type listResponse struct {
	Items       []db.TidyItem `json:"items"`
	Orphans     []Orphan      `json:"orphans"`
	Count       int           `json:"count"`
	Total       int           `json:"total"`
	Limit       int           `json:"limit"`
	ReviewAfter string        `json:"review_after"`
	Enabled     bool          `json:"enabled"`
}

func getList(t *testing.T) listResponse {
	t.Helper()
	w := httptest.NewRecorder()
	HandleList(w, httptest.NewRequest("GET", "/api/tidyup", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp listResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func post(t *testing.T, h http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return w
}

func listed(items []db.TidyItem, id string) bool {
	for _, it := range items {
		if it.ID == id {
			return true
		}
	}
	return false
}

func TestHandleListOffersIdleChatsAndLeavesTheRestAlone(t *testing.T) {
	setupDB(t)
	config.Workspace = t.TempDir()
	enableReview(t, 30*24*time.Hour, 30*24*time.Hour)

	idleChat(t, "old", "Old work", "", 45*24*time.Hour)
	idleChat(t, "recent", "This week", "", 3*24*time.Hour)

	resp := getList(t)

	if !resp.Enabled {
		t.Error("expected the surface to report itself enabled")
	}
	if !listed(resp.Items, "old") {
		t.Error("a chat idle for 45 days should be offered")
	}
	if listed(resp.Items, "recent") {
		t.Error("a chat used three days ago must not be offered")
	}
	if resp.Count != 1 || resp.Total != 1 {
		t.Errorf("count/total = %d/%d, want 1/1", resp.Count, resp.Total)
	}
	if resp.ReviewAfter == "" {
		t.Error("the window should be reported so the UI can explain why an item is here")
	}
	// Listing decides nothing: the chat is still there afterwards.
	if !db.SessionExists("old") {
		t.Error("listing must not delete anything")
	}
}

// The switch has to turn the whole surface off, not just the sweeper.
func TestHandleListSaysNothingWhenHygieneIsDisabled(t *testing.T) {
	setupDB(t)
	config.Workspace = t.TempDir()
	enableReview(t, 30*24*time.Hour, 30*24*time.Hour)
	config.HygieneEnabled = false

	idleChat(t, "old", "Old work", "", 45*24*time.Hour)

	resp := getList(t)
	if resp.Enabled {
		t.Error("expected enabled=false")
	}
	if len(resp.Items) != 0 || resp.Total != 0 {
		t.Errorf("expected an empty list when disabled, got %d items / total %d", len(resp.Items), resp.Total)
	}
}

// A capped list must still admit how much it is not showing, or the user
// reasonably concludes they are done after one pass.
func TestHandleListCapsTheListButReportsTheTotal(t *testing.T) {
	setupDB(t)
	config.Workspace = t.TempDir()
	enableReview(t, 30*24*time.Hour, 30*24*time.Hour)

	const created = db.ReviewListLimit + 8
	for i := 0; i < created; i++ {
		idleChat(t, "s"+strconv.Itoa(i), "Chat", "", time.Duration(40+i)*24*time.Hour)
	}

	resp := getList(t)
	if resp.Count != db.ReviewListLimit || len(resp.Items) != db.ReviewListLimit {
		t.Errorf("listed %d items, want the limit of %d", len(resp.Items), db.ReviewListLimit)
	}
	if resp.Total != created {
		t.Errorf("total = %d, want %d", resp.Total, created)
	}
	if resp.Limit != db.ReviewListLimit {
		t.Errorf("limit = %d, want %d", resp.Limit, db.ReviewListLimit)
	}
}

// Showing a size next to a folder implies we would delete it. For a directory
// the user chose themselves we would not, so we say nothing about it.
func TestHandleListReportsAFolderOnlyWhenItWouldBeDeleted(t *testing.T) {
	setupDB(t)
	base := t.TempDir()
	config.Workspace = base
	enableReview(t, 30*24*time.Hour, 30*24*time.Hour)

	autoDir := filepath.Join(base, "chat-aaaaaaaaaaaa")
	if err := os.MkdirAll(autoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(autoDir, "notes.txt"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	userDir := filepath.Join(base, "my-important-files")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "notes.txt"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}

	idleChat(t, "auto", "Auto", autoDir, 45*24*time.Hour)
	idleChat(t, "mine", "Mine", userDir, 45*24*time.Hour)

	resp := getList(t)
	for _, it := range resp.Items {
		switch it.ID {
		case "auto":
			if it.Workdir != autoDir {
				t.Errorf("auto workdir = %q, want %q", it.Workdir, autoDir)
			}
			if it.SizeBytes != 10 {
				t.Errorf("size = %d, want 10", it.SizeBytes)
			}
		case "mine":
			if it.Workdir != "" {
				t.Errorf("a user-chosen folder must not be reported: got %q", it.Workdir)
			}
			if it.SizeBytes != 0 {
				t.Errorf("a folder we will not delete must not be measured: got %d", it.SizeBytes)
			}
		}
	}
}

// "Keep for now" defers the question. It must not silently answer it forever —
// that would build a second invisible pile of chats nobody is ever asked about.
func TestHandleKeepDefersTheChatRatherThanExemptingIt(t *testing.T) {
	setupDB(t)
	config.Workspace = t.TempDir()
	enableReview(t, 30*24*time.Hour, 30*24*time.Hour)
	idleChat(t, "s1", "Old work", "", 45*24*time.Hour)

	if !listed(getList(t).Items, "s1") {
		t.Fatal("expected the chat to be offered before snoozing")
	}

	w := post(t, HandleKeep, "/api/tidyup/keep", `{"ids":["s1"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	resp := getList(t)
	if listed(resp.Items, "s1") || resp.Total != 0 {
		t.Error("a deferred chat must drop out of the current review")
	}
	if cfg, _ := db.GetSessionConfig("s1"); cfg.Keep {
		t.Error("deferring must not set the permanent Keep flag")
	}
	if !db.SessionExists("s1") {
		t.Error("deferring must never delete the chat")
	}

	// Once the snooze elapses it comes back.
	if err := db.SnoozeSession("s1", time.Now().Add(-time.Hour), time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	if !listed(getList(t).Items, "s1") {
		t.Error("the chat should be offered again after the snooze window")
	}
}

func TestHandleKeepAcceptsASingleID(t *testing.T) {
	setupDB(t)
	config.Workspace = t.TempDir()
	enableReview(t, 30*24*time.Hour, 30*24*time.Hour)
	idleChat(t, "s1", "Old work", "", 45*24*time.Hour)

	w := post(t, HandleKeep, "/api/tidyup/keep", `{"id":"s1"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if listed(getList(t).Items, "s1") {
		t.Error("expected the single id to be deferred")
	}
}

func TestHandleCleanupRemovesTheChatAndItsAutoWorkdir(t *testing.T) {
	setupDB(t)
	base := t.TempDir()
	config.Workspace = base
	chatDir := filepath.Join(base, "chat-abc123abc123")
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession("c1", config.SessionConfig{Label: "x", Workdir: chatDir}); err != nil {
		t.Fatal(err)
	}

	w := post(t, HandleCleanup, "/api/tidyup/cleanup", `{"items":[{"kind":"chat","id":"c1"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if db.SessionExists("c1") {
		t.Error("the chat record should be gone")
	}
	if _, err := os.Stat(chatDir); !os.IsNotExist(err) {
		t.Error("its auto-created workspace should be gone too")
	}
}

func TestHandleCleanupLeavesAUserChosenWorkdirAlone(t *testing.T) {
	setupDB(t)
	base := t.TempDir()
	config.Workspace = base
	userDir := filepath.Join(base, "my-important-files")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession("c2", config.SessionConfig{Label: "x", Workdir: userDir}); err != nil {
		t.Fatal(err)
	}

	w := post(t, HandleCleanup, "/api/tidyup/cleanup", `{"items":[{"kind":"chat","id":"c2"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if _, err := os.Stat(userDir); err != nil {
		t.Errorf("a directory the user chose must survive deleting the chat: %v", err)
	}
}

func TestHandleCleanupRemovesAConfirmedOrphan(t *testing.T) {
	setupDB(t)
	base := t.TempDir()
	config.Workspace = base
	// One live chat, so the session set is non-empty and the scan is trusted.
	live := filepath.Join(base, "chat-liveliveliv")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession("live", config.SessionConfig{Workdir: live}); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(base, "chat-orphanorpha")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}

	w := post(t, HandleCleanup, "/api/tidyup/cleanup",
		`{"items":[{"kind":"orphan","path":`+strconv.Quote(orphan)+`}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("the confirmed orphan should be removed")
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("the live chat's folder must survive: %v", err)
	}
}

// The path comes from the browser and is not trusted. Every one of these is a
// directory the endpoint must refuse even though the caller asked nicely.
func TestHandleCleanupRefusesPathsOutsideTheRails(t *testing.T) {
	setupDB(t)
	base := t.TempDir()
	config.Workspace = base

	live := filepath.Join(base, "chat-liveliveliv")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession("live", config.SessionConfig{Workdir: live}); err != nil {
		t.Fatal(err)
	}

	outside := t.TempDir()
	notAChat := filepath.Join(base, "my-important-files")
	nested := filepath.Join(base, "chat-liveliveliv", "chat-nestednest")
	for _, d := range []string{filepath.Join(outside, "chat-outsideout"), notAChat, nested} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cases := map[string]string{
		"a chat folder outside the workspace": filepath.Join(outside, "chat-outsideout"),
		"a folder that is not a chat folder":  notAChat,
		"a chat folder nested inside another": nested,
		"the workspace itself":                base,
		"a live chat's folder":                live,
		"an empty path":                       "",
		// Dressed up to look like it is inside the workspace.
		"traversal out of the workspace": filepath.Join(base, "chat-liveliveliv", "..", "..",
			filepath.Base(outside), "chat-outsideout"),
	}

	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			w := post(t, HandleCleanup, "/api/tidyup/cleanup",
				`{"items":[{"kind":"orphan","path":`+strconv.Quote(path)+`}]}`)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 with a per-item error, got %d", w.Code)
			}
			var resp struct {
				Results []cleanupResult `json:"results"`
				Removed int             `json:"removed"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.Removed != 0 || len(resp.Results) != 1 || resp.Results[0].Removed {
				t.Fatalf("%s must be refused, got %+v", name, resp.Results)
			}
			if resp.Results[0].Error == "" {
				t.Error("a refusal should say why")
			}
			if path != "" {
				if _, err := os.Stat(path); err != nil {
					t.Errorf("%s must still exist: %v", name, err)
				}
			}
		})
	}
}

func TestHandleCleanupReportsUnknownKindsWithoutFailingTheBatch(t *testing.T) {
	setupDB(t)
	base := t.TempDir()
	config.Workspace = base
	if err := db.CreateSession("c1", config.SessionConfig{Label: "x"}); err != nil {
		t.Fatal(err)
	}

	w := post(t, HandleCleanup, "/api/tidyup/cleanup",
		`{"items":[{"kind":"bogus","id":"x"},{"kind":"chat","id":"c1"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (per-item errors), got %d", w.Code)
	}
	var resp struct {
		Results []cleanupResult `json:"results"`
		Removed int             `json:"removed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected a result per item, got %d", len(resp.Results))
	}
	if resp.Results[0].Removed || resp.Results[0].Error == "" {
		t.Errorf("the unknown kind should be reported as an error: %+v", resp.Results[0])
	}
	// One bad item must not prevent the rest of the user's selection.
	if !resp.Results[1].Removed || resp.Removed != 1 {
		t.Errorf("the valid item should still be removed: %+v", resp.Results[1])
	}
}

func TestHandleCleanupRejectsInvalidJSON(t *testing.T) {
	setupDB(t)
	w := post(t, HandleCleanup, "/api/tidyup/cleanup", `{"items":`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// The badge counts everything waiting, not the capped page — a badge reading
// "50" beside 145 idle chats would understate the backlog forever.
func TestHandleCountReportsTheFullBacklog(t *testing.T) {
	setupDB(t)
	config.Workspace = t.TempDir()
	enableReview(t, 30*24*time.Hour, 30*24*time.Hour)

	const created = db.ReviewListLimit + 5
	for i := 0; i < created; i++ {
		idleChat(t, "s"+strconv.Itoa(i), "Chat", "", time.Duration(40+i)*24*time.Hour)
	}
	idleChat(t, "recent", "Recent", "", 2*24*time.Hour)

	w := httptest.NewRecorder()
	HandleCount(w, httptest.NewRequest("GET", "/api/tidyup/count", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Count   int  `json:"count"`
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != created {
		t.Errorf("count = %d, want %d", resp.Count, created)
	}
	if !resp.Enabled {
		t.Error("expected enabled=true")
	}
}

func TestHandleCountIsZeroWhenHygieneIsDisabled(t *testing.T) {
	setupDB(t)
	config.Workspace = t.TempDir()
	enableReview(t, 30*24*time.Hour, 30*24*time.Hour)
	config.HygieneEnabled = false
	idleChat(t, "old", "Old work", "", 45*24*time.Hour)

	w := httptest.NewRecorder()
	HandleCount(w, httptest.NewRequest("GET", "/api/tidyup/count", nil))
	var resp struct {
		Count   int  `json:"count"`
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 0 || resp.Enabled {
		t.Errorf("disabled hygiene must show no badge, got %+v", resp)
	}
}

// A shared chat is someone else's too, so cleanup refuses it even
// when asked for it by id, and it stays exactly as it was.
func TestCleanupRefusesASharedChat(t *testing.T) {
	setupDB(t)
	idleChat(t, "shared-1", "Launch plan", "", 400*24*time.Hour)
	db.SetSessionShared("shared-1", true)
	w := post(t, HandleCleanup, "/api/tidyup/cleanup", `{"items":[{"kind":"chat","id":"shared-1"}]}`)
	var body struct {
		Removed int             `json:"removed"`
		Results []cleanupResult `json:"results"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body.Removed != 0 || len(body.Results) != 1 || body.Results[0].Removed || !strings.Contains(body.Results[0].Error, "shared") {
		t.Errorf("cleanup of a shared chat = %s", w.Body.String())
	}
	if !db.SessionExists("shared-1") || !db.IsSessionShared("shared-1") {
		t.Error("the shared chat was touched")
	}
}
