// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package fileshare

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/fileshare/viewer"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/workspaceapps"
)

// previewOn runs as on the platform, with the preview on a free port.
func previewOn(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	t.Setenv("KWA_PREVIEW_PORT", strconv.Itoa(port))
	t.Setenv("STRONG_NETWORK_WORKSPACE_ID", "7")
	t.Setenv("STRONG_NETWORK_WORKSPACE_APPS_DOMAIN", "proxy.example")
	t.Cleanup(func() { viewer.Unbind(port) })
	return port
}

func (r *rig) previewLink(t *testing.T, path string, withToken bool) (map[string]string, *httptest.ResponseRecorder) {
	t.Helper()
	rec := call(t, HandlePreviewLink, "POST", "/api/files/preview-link", "", previewRequest{Path: path}, withToken)
	out := map[string]string{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out, rec
}

func TestAPreviewLinkMakesTheOwnersOnlyAppOnce(t *testing.T) {
	r := setup(t)
	port := previewOn(t)
	origin := fmt.Sprintf("https://ws-7-port-%d.proxy.example", port)
	out, rec := r.previewLink(t, filepath.Join(r.folder, "report.html"), true)
	if rec.Code != http.StatusOK || out["origin"] != origin || !strings.HasPrefix(out["url"], origin+"/p/") {
		t.Fatalf("link: %d %s", rec.Code, rec.Body)
	}
	if exp, err := time.Parse(time.RFC3339, out["expires"]); err != nil || time.Until(exp) < 59*time.Minute {
		t.Errorf("expires: %q", out["expires"])
	}
	app, ok := r.p.appOn(port)
	if !ok || app.Name != previewAppName || app.IsSharedWithTeam || len(app.UserIDs) != 0 {
		t.Fatalf("preview app: %+v", app)
	}
	again, rec := r.previewLink(t, filepath.Join(r.folder, "report.html"), true)
	if rec.Code != http.StatusOK || again["url"] == out["url"] || r.p.count("create") != 1 {
		t.Errorf("second link: %d %s; %d creates", rec.Code, rec.Body, r.p.count("create"))
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s/content", port, strings.TrimPrefix(out["url"], origin)))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasSuffix(string(b), "</script><h1>Q3</h1>") {
		t.Errorf("the link served %d %q", resp.StatusCode, b)
	}
	if r.p.badTokens != 0 {
		t.Error("a platform call went without the owner's token")
	}
}

func TestWithoutAPreviewAppScriptsAreOff(t *testing.T) {
	for name, c := range map[string]struct {
		env      map[string]string
		path     string
		noToken  bool
		platform error
		status   int
		says     string
	}{
		"no credential":       {noToken: true, status: http.StatusForbidden, says: scriptsOff + "this request did not carry"},
		"outside a workspace": {env: map[string]string{"STRONG_NETWORK_WORKSPACE_ID": ""}, status: http.StatusConflict, says: scriptsOff + "Knowledge Worker Agent isn't running in a workspace."},
		"turned off":          {env: map[string]string{"KWA_PREVIEW_PORT": "off"}, status: http.StatusConflict, says: scriptsOff + "they're turned off in this workspace."},
		"not a port":          {env: map[string]string{"KWA_PREVIEW_PORT": "11111"}, status: http.StatusConflict, says: scriptsOff + `KWA_PREVIEW_PORT="11111" is not a port`},
		"platform down":       {platform: errors.New("dial unix: no such file"), status: http.StatusBadGateway, says: scriptsOff + "the workspace platform did not answer."},
		"not HTML":            {path: "notes.md", status: http.StatusConflict, says: "Only HTML files run their scripts in the preview."},
		"missing":             {path: "gone.html", status: http.StatusConflict, says: "The file is no longer there."},
		"relative":            {path: "-", status: http.StatusBadRequest, says: "The path must be absolute."},
	} {
		t.Run(name, func(t *testing.T) {
			r := setup(t)
			previewOn(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			write(t, filepath.Join(r.folder, "notes.md"), "# Notes")
			path := filepath.Join(r.folder, "report.html")
			switch c.path {
			case "-":
				path = "report.html"
			case "":
			default:
				path = filepath.Join(r.folder, c.path)
			}
			r.p.failWith = c.platform
			out, rec := r.previewLink(t, path, !c.noToken)
			if rec.Code != c.status || !strings.HasPrefix(out["error"], c.says) {
				t.Errorf("%d %s", rec.Code, rec.Body)
			}
			if r.p.count("create") != 0 || out["url"] != "" {
				t.Error("a preview was set up")
			}
		})
	}
}

func TestThePreviewNeverTakesAnotherApp(t *testing.T) {
	r := setup(t)
	port := previewOn(t)
	mine := workspaceapps.App{ID: 9, Port: uint32(port), Name: "My dev server", UserIDs: []uint64{2}}
	r.p.apps = append(r.p.apps, mine)
	out, rec := r.previewLink(t, filepath.Join(r.folder, "report.html"), true)
	if rec.Code != http.StatusConflict || out["error"] != fmt.Sprintf(scriptsOff+"port %d belongs to another app, %q.", port, "My dev server") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if app, _ := r.p.appOn(port); r.p.count("edit")+r.p.count("delete")+r.p.count("create") != 0 || app.Name != mine.Name || len(app.UserIDs) != 1 {
		t.Errorf("the other app was changed: %+v", app)
	}
}

func TestAPreviewAppSharedWithOthersIsMadeTheOwnersAgain(t *testing.T) {
	r := setup(t)
	port := previewOn(t)
	r.p.apps = append(r.p.apps, workspaceapps.App{ID: 9, Port: uint32(port), Name: previewAppName, UserIDs: []uint64{2}, IsSharedWithTeam: true})
	if _, rec := r.previewLink(t, filepath.Join(r.folder, "report.html"), true); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	e := r.p.lastEdit
	if r.p.count("edit") != 1 || e.ID != 9 || e.Name != previewAppName || e.Port != uint32(port) || e.IsSharedWithTeam || len(e.UserIDs) != 0 {
		t.Errorf("edit: %d %+v", r.p.count("edit"), e)
	}
}

func TestAPreviewPortInUseTurnsScriptsOff(t *testing.T) {
	r := setup(t)
	port := previewOn(t)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Skip("port taken meanwhile")
	}
	defer ln.Close()
	out, rec := r.previewLink(t, filepath.Join(r.folder, "report.html"), true)
	if rec.Code != http.StatusConflict || out["error"] != fmt.Sprintf(scriptsOff+"port %d is in use.", port) || r.p.count("create") != 0 {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}
