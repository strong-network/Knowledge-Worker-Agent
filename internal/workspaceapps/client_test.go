// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package workspaceapps

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// The platform proxy sends the header as literal lower-case with underscores.
// Some servers drop underscore headers; prove Go's does not, using raw bytes on
// the wire rather than a Go client, which would canonicalise the name first.
func TestOwnerTokenSurvivesTheWireFormatTheProxySends(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- OwnerToken(r)
	}))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /api/share/platform HTTP/1.1\r\nHost: x\r\ncloud_editor_user_token: tok-abc\r\nConnection: close\r\n\r\n")
	if _, err := http.ReadResponse(bufio.NewReader(conn), nil); err != nil {
		t.Fatal(err)
	}
	if v := <-got; v != "tok-abc" {
		t.Fatalf("OwnerToken = %q, want the header value", v)
	}
}

// fakeSidecar serves an http.Handler on a short-path unix socket and points the
// client at it.
func fakeSidecar(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sc")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	prev := socketPath
	socketPath = sock
	t.Cleanup(func() {
		client.CloseIdleConnections()
		srv.Close()
		socketPath = prev
		os.RemoveAll(dir)
	})
}

// A reply shaped as grpc-gateway v2 writes it: camelCase, uint64 as strings,
// unpopulated fields emitted, plus fields this client does not read.
const listReply = `{"workspaceApps":[{"id":"42","port":8766,"name":"Knowledge Worker Agent","url":"https://ws-7-port-8766.proxy.example","online":true,"userIds":["1001","1002"],"isPublic":false,"isSharedWithTeam":false,"isHttpsEnabled":false,"overrideHost":false,"overrideHostValue":""}]}`

func TestListSendsTheTokenAndParsesTheGatewayShape(t *testing.T) {
	fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/internal/v1/workspace_app:list" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("workspacetoken") != "tok-1" {
			t.Errorf("workspacetoken header = %q", r.Header.Get("workspacetoken"))
		}
		io.WriteString(w, listReply)
	})
	apps, err := List(t.Context(), "tok-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 {
		t.Fatalf("got %d apps", len(apps))
	}
	a := apps[0]
	if a.ID != 42 || a.Port != 8766 || a.Name != "Knowledge Worker Agent" || !a.Online || a.URL == "" {
		t.Errorf("parsed %+v", a)
	}
	if len(a.UserIDs) != 2 || a.UserIDs[0] != 1001 {
		t.Errorf("user ids %v", a.UserIDs)
	}
}

func TestListReportsTheSidecarRefusal(t *testing.T) {
	fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"code":16,"message":"[ListWorkspaceApps] Token is blocked","details":[]}`)
	})
	_, err := List(t.Context(), "bad")
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("want *Error, got %v", err)
	}
	if se.Status != 401 || se.Code != 16 || !strings.Contains(se.Message, "Token is blocked") {
		t.Errorf("got %+v", se)
	}
	if strings.Contains(se.Error(), "Token is blocked") {
		t.Error("Error() carries the sidecar message, which would then reach the log")
	}
}

func TestPlatformCheckWithoutTokenDoesNotCallTheSidecar(t *testing.T) {
	var calls atomic.Int32
	fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })

	w := httptest.NewRecorder()
	HandlePlatformCheck(w, httptest.NewRequest("GET", "/api/share/platform", nil))
	var body struct {
		OwnerToken bool `json:"owner_token"`
		Sidecar    struct {
			OK bool `json:"ok"`
		} `json:"sidecar"`
		Apps []App `json:"apps"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.OwnerToken || body.Sidecar.OK || calls.Load() != 0 {
		t.Errorf("owner_token=%v ok=%v calls=%d", body.OwnerToken, body.Sidecar.OK, calls.Load())
	}
	if body.Apps == nil {
		t.Error("apps must encode as [] not null")
	}
}

// The token is the owner's platform credential: it must not come back in the
// response or go to the log, on success or on failure.
func TestPlatformCheckNeverEchoesOrLogsTheToken(t *testing.T) {
	const secret = "tok-SECRET-9f3a"
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	for _, status := range []int{200, 401} {
		fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
			if status != 200 {
				w.WriteHeader(status)
				io.WriteString(w, `{"code":16,"message":"blocked"}`)
				return
			}
			io.WriteString(w, listReply)
		})
		req := httptest.NewRequest("GET", "/api/share/platform", nil)
		req.Header.Set(ownerTokenHeader, secret)
		w := httptest.NewRecorder()
		HandlePlatformCheck(w, req)
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("status %d: response contains the token", status)
		}
		if !strings.Contains(w.Body.String(), `"owner_token":true`) {
			t.Errorf("status %d: owner_token not reported: %s", status, w.Body.String())
		}
	}
	if strings.Contains(logs.String(), secret) {
		t.Error("the token reached the log")
	}
}

func TestPlatformCheckReportsAnUnreachableSidecar(t *testing.T) {
	prev := socketPath
	socketPath = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { socketPath = prev; client.CloseIdleConnections() })

	req := httptest.NewRequest("GET", "/api/share/platform", nil)
	req.Header.Set(ownerTokenHeader, "tok")
	w := httptest.NewRecorder()
	HandlePlatformCheck(w, req)
	if !strings.Contains(w.Body.String(), `"ok":false`) || !strings.Contains(w.Body.String(), `"error"`) {
		t.Errorf("unreachable sidecar not reported: %s", w.Body.String())
	}
}

// recordingSidecar answers each route with reply and records the request bodies.
func recordingSidecar(t *testing.T, reply map[string]string) map[string]map[string]any {
	t.Helper()
	got := map[string]map[string]any{}
	fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("workspacetoken") != "tok" {
			t.Errorf("%s: workspacetoken = %q", r.URL.Path, r.Header.Get("workspacetoken"))
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		got[r.Method+" "+r.URL.Path] = body
		io.WriteString(w, reply[r.URL.Path])
	})
	return got
}

func TestCreateMakesAPrivatePlainHTTPApp(t *testing.T) {
	got := recordingSidecar(t, map[string]string{
		"/internal/v1/workspace_app:create": `{"id":"42","port":8766,"name":"Knowledge Worker Agent","url":"https://ws-7-port-8766.proxy.example","online":true}`,
	})
	a, err := Create(t.Context(), "tok", 8766, "Knowledge Worker Agent", false, []uint64{1001, 9007199254740993})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != 42 || a.URL == "" || !a.Online {
		t.Errorf("parsed %+v", a)
	}
	b := got["POST /internal/v1/workspace_app:create"]
	if b["isPublic"] != false || b["isHttpsEnabled"] != false || b["overrideHost"] != false || b["port"] != "8766" || b["appName"] != "Knowledge Worker Agent" {
		t.Errorf("create body %v", b)
	}
	// Ids travel as strings: 2^53+1 would not survive a float.
	if ids, _ := b["sharedWith"].([]any); len(ids) != 2 || ids[1] != "9007199254740993" {
		t.Errorf("sharedWith %v", b["sharedWith"])
	}
}

// The platform's edit assigns every field from the request, so what is
// sent back must be the listed app whole, with only the audience changed.
func TestEditSendsEveryFieldOfTheListedApp(t *testing.T) {
	got := recordingSidecar(t, map[string]string{
		"/internal/v1/workspace_app:list": `{"workspaceApps":[{"id":"42","port":8766,"name":"Kept name","url":"u","online":true,"userIds":["1"],"isPublic":false,"isSharedWithTeam":true,"isHttpsEnabled":true,"overrideHost":true,"overrideHostValue":"example.internal"}]}`,
		"/internal/v1/workspace_app:edit": `{"id":"42"}`,
	})
	apps, err := List(t.Context(), "tok")
	if err != nil || len(apps) != 1 {
		t.Fatal(err, apps)
	}
	a := apps[0]
	a.UserIDs = []uint64{1, 2}
	if err := Edit(t.Context(), "tok", a); err != nil {
		t.Fatal(err)
	}
	b := got["POST /internal/v1/workspace_app:edit"]
	want := map[string]any{
		"wsAppId": "42", "appName": "Kept name", "isPublic": false, "projectSharing": true,
		"isHttpsEnabled": true, "overrideHost": true, "overrideHostValue": "example.internal",
	}
	for k, v := range want {
		if b[k] != v {
			t.Errorf("edit %s = %v, want %v", k, b[k], v)
		}
	}
	if ids, _ := b["sharedWith"].([]any); len(ids) != 2 || ids[0] != "1" || ids[1] != "2" {
		t.Errorf("sharedWith %v", b["sharedWith"])
	}
}

func TestDeleteAndMembers(t *testing.T) {
	got := recordingSidecar(t, map[string]string{
		"/internal/v1/workspace_app:delete": `{}`,
		"/internal/v1/project/users":        `{"users":[{"userId":"1001","email":"sarah@example.com","isInWorkspace":false},{"userId":"7","email":"owner@example.com"}]}`,
	})
	if err := Delete(t.Context(), "tok", 42); err != nil {
		t.Fatal(err)
	}
	if b := got["POST /internal/v1/workspace_app:delete"]; b["appId"] != "42" {
		t.Errorf("delete body %v", b)
	}
	m, err := Members(t.Context(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m[0].ID != 1001 || m[0].Email != "sarah@example.com" {
		t.Errorf("members %+v", m)
	}
}
