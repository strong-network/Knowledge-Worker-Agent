// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"log"
	"net"
	"net/http"
	"syscall"
)

// refuseCrossOrigin refuses a request that can change something (any method
// but GET, HEAD and OPTIONS) when a browser sends it from another origin. The
// API has no authentication, so a page on any site the owner visits could
// otherwise act through their browser: start an agent, add a connector that
// runs a command, write files. A browser says where a request comes from
// (Sec-Fetch-Site, or Origin against Host); a client outside one, such as the
// agent's own tools, sends neither and isn't refused.
func refuseCrossOrigin(h http.Handler) http.Handler {
	p := http.NewCrossOriginProtection()
	p.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Refused a cross-origin %s %s (Sec-Fetch-Site %q, Origin %q)", r.Method, r.URL.Path, r.Header.Get("Sec-Fetch-Site"), r.Header.Get("Origin"))
		http.Error(w, "Cross-origin request refused", http.StatusForbidden)
	}))
	return p.Handler(h)
}

// listenAll listens on every address. Any but the first may be missing, as
// ::1 is where IPv6 is off, and then nothing can connect over it either.
func listenAll(addrs []string) ([]net.Listener, error) {
	var ls []net.Listener
	for i, a := range addrs {
		l, err := net.Listen("tcp", a)
		if err == nil {
			ls = append(ls, l)
			continue
		}
		if i > 0 && (errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.EAFNOSUPPORT)) {
			log.Printf("Not listening on %s: %v", a, err)
			continue
		}
		for _, l := range ls {
			l.Close()
		}
		return nil, err
	}
	return ls, nil
}
