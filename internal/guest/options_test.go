// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package guest

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// A toggle in the owner's modal reaches a guest page already open.
func TestShareOptionsReachAnOpenGuestPage(t *testing.T) {
	e := newEnv(t)
	e.identify("Sarah")
	req, _ := http.NewRequest("GET", e.srv.URL+"/guest/api/sessions/"+e.shared+"/events", nil)
	resp, err := e.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	opts := make(chan map[string]any, 8)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if l := sc.Text(); strings.HasPrefix(l, "data: ") && strings.Contains(l, `"share_options"`) {
				var m map[string]any
				json.Unmarshal([]byte(l[len("data: "):]), &m)
				opts <- m
			}
		}
	}()
	next := func() map[string]any {
		select {
		case m := <-opts:
			return m
		case <-time.After(3 * time.Second):
			t.Fatal("no share_options frame")
		}
		return nil
	}
	if m := next(); m["allow_files"] != false || m["allow_permissions"] != false {
		t.Errorf("initial options %v", m)
	}
	db.SetShareOptions(e.shared, db.ShareOptions{AllowFiles: true})
	chat.ShareOptionsChanged(e.shared)
	if m := next(); m["allow_files"] != true || m["allow_permissions"] != false {
		t.Errorf("after the change %v", m)
	}
	if code, body, _ := e.do("GET", "/guest/api/sessions/"+e.shared, "", nil); code != 200 || !strings.Contains(body, `"allow_files":true`) {
		t.Errorf("meta %d %s", code, body)
	}
}
