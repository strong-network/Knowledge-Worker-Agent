// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package fileshare

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

func canShare(t *testing.T, folder, file string) (canShareResponse, int) {
	t.Helper()
	q := url.Values{"folder": {folder}}
	if file != "" {
		q.Set("file", file)
	}
	rec := call(t, HandleCanShare, "GET", "/api/artifacts/can-share?"+q.Encode(), "", nil, false)
	var resp canShareResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("%v: %s", err, rec.Body)
		}
	}
	return resp, rec.Code
}

func chatIn(t *testing.T, id, folder string, temporary bool) {
	t.Helper()
	cfg := config.DefaultSessionConfig()
	cfg.Workdir, cfg.Temporary = folder, temporary
	if err := db.CreateSession(id, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestFilesInAChatsFolderCanBeShared(t *testing.T) {
	r := setup(t)
	for _, where := range []string{r.folder, filepath.Join(r.folder, "sub")} {
		if resp, code := canShare(t, where, ""); code != 200 || !resp.CanShare || resp.Reason != "" {
			t.Errorf("%s: %d %+v", where, code, resp)
		}
	}
}

func TestAFileIsCheckedAsSharingWould(t *testing.T) {
	r := setup(t)
	write(t, filepath.Join(r.folder, "sub", "cdn.html"), `<script src="https://cdn.jsdelivr.net/npm/chart.js"></script>`)
	for _, c := range []struct{ where, file string }{
		{r.folder, "sub/cdn.html"},
		{filepath.Join(r.folder, "sub"), "cdn.html"},
		{r.folder, filepath.Join(r.folder, "sub", "cdn.html")},
	} {
		resp, _ := canShare(t, c.where, c.file)
		if resp.SelfContained == nil || *resp.SelfContained || len(resp.Findings) != 1 || !resp.Findings[0].Blocking ||
			!strings.Contains(resp.Findings[0].What, "cdn.jsdelivr.net") {
			t.Errorf("%s in %s: %+v", c.file, c.where, resp)
		}
	}
	if resp, _ := canShare(t, r.folder, "report.html"); resp.SelfContained == nil || !*resp.SelfContained || resp.Problem != "" {
		t.Errorf("report.html: %+v", resp)
	}
}

func TestOnlyAFileInTheChatsFolderIsChecked(t *testing.T) {
	r := setup(t)
	write(t, filepath.Join(r.folder, ".system", "x.html"), "<p>hidden</p>")
	write(t, filepath.Join(filepath.Dir(r.folder), "outside.html"), "<p>outside</p>")
	outside := filepath.Join(filepath.Dir(r.folder), "outside.html")
	for _, file := range []string{"../outside.html", outside, r.folder} {
		if resp, _ := canShare(t, r.folder, file); resp.SelfContained != nil || resp.Problem != "Only a file in a chat's folder can be checked." {
			t.Errorf("%s: %+v", file, resp)
		}
	}
	for _, file := range []string{".system/x.html", "missing.html", "notes.txt"} {
		if resp, _ := canShare(t, r.folder, file); resp.SelfContained != nil || resp.Problem == "" {
			t.Errorf("%s: %+v", file, resp)
		}
	}
}

func TestATemporaryChatsFilesCantBeShared(t *testing.T) {
	r := setup(t)
	// A temporary chat's folder inside another chat's: the nearest folder decides.
	tmp := filepath.Join(r.folder, "Chats", "chat-tmp")
	chatIn(t, "chat-tmp", tmp, true)
	resp, _ := canShare(t, tmp, "")
	if resp.CanShare || !strings.Contains(resp.Reason, "temporary") {
		t.Errorf("%+v", resp)
	}
	// A folder that a kept chat works in too can be shared.
	chatIn(t, "chat-kept", tmp, false)
	if resp, _ := canShare(t, tmp, ""); !resp.CanShare {
		t.Errorf("with a kept chat: %+v", resp)
	}
}

func TestNothingCanBeSharedWhenSharingIsOff(t *testing.T) {
	r := setup(t)
	t.Setenv("KWA_ARTIFACT_PORTS", "off")
	resp, _ := canShare(t, r.folder, "report.html")
	if resp.CanShare || resp.Reason != "Sharing files is turned off in this workspace." {
		t.Errorf("off: %+v", resp)
	}
	if resp.SelfContained == nil || !*resp.SelfContained {
		t.Errorf("the file is still checked: %+v", resp)
	}
	t.Setenv("KWA_ARTIFACT_PORTS", "nine")
	if resp, _ := canShare(t, r.folder, ""); resp.CanShare || !strings.Contains(resp.Reason, "KWA_ARTIFACT_PORTS") {
		t.Errorf("bad value: %+v", resp)
	}
}

func TestAFolderNoChatWorksInCantBeShared(t *testing.T) {
	setup(t)
	elsewhere := t.TempDir()
	write(t, filepath.Join(elsewhere, "page.html"), "<p>page</p>")
	resp, _ := canShare(t, elsewhere, "page.html")
	if resp.CanShare || resp.Reason != "This folder isn't a chat's folder." || resp.SelfContained != nil || resp.Problem == "" {
		t.Errorf("%+v", resp)
	}
	if _, code := canShare(t, "relative/folder", ""); code != http.StatusBadRequest {
		t.Errorf("a relative folder: %d", code)
	}
}
