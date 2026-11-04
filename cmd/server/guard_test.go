// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A page on another site or origin can't change anything through the owner's
// browser; the app itself, reads, and clients outside a browser still can.
func TestCrossOriginWritesAreRefused(t *testing.T) {
	h := refuseCrossOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, c := range []struct {
		name, method, site, origin string
		want                       int
	}{
		{"the app itself", "POST", "same-origin", "https://kwa.apps.example.com", http.StatusNoContent},
		{"typed or bookmarked", "POST", "none", "", http.StatusNoContent},
		{"outside a browser", "POST", "", "", http.StatusNoContent},
		{"another site", "POST", "cross-site", "https://evil.example", http.StatusForbidden},
		{"another app on the same site", "PUT", "same-site", "https://other.apps.example.com", http.StatusForbidden},
		{"a form from another site", "POST", "cross-site", "null", http.StatusForbidden},
		{"delete from another site", "DELETE", "cross-site", "https://evil.example", http.StatusForbidden},
		{"an older browser, another origin", "POST", "", "https://evil.example", http.StatusForbidden},
		{"an older browser, same origin", "POST", "", "http://127.0.0.1:8765", http.StatusNoContent},
		{"a read from another site", "GET", "cross-site", "https://evil.example", http.StatusNoContent},
	} {
		req := httptest.NewRequest(c.method, "http://127.0.0.1:8765/api/mcp/servers", strings.NewReader("{}"))
		if c.site != "" {
			req.Header.Set("Sec-Fetch-Site", c.site)
		}
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

func TestListenAllNeedsOnlyTheFirstAddress(t *testing.T) {
	ls, err := listenAll([]string{"127.0.0.1:0", "192.0.2.1:0"})
	if err != nil || len(ls) != 1 {
		t.Fatalf("an address the machine doesn't have stopped the rest: %v %v", ls, err)
	}
	ls[0].Close()

	if _, err := listenAll([]string{"192.0.2.1:0", "127.0.0.1:0"}); err == nil {
		t.Error("the first address failing must fail")
	}

	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	if ls, err := listenAll([]string{first.Addr().String(), taken.Addr().String()}); err == nil {
		for _, l := range ls {
			l.Close()
		}
		t.Error("a port in use on a later address must fail, or another server answers there")
	}
}
