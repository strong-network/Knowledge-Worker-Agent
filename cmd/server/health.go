// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/env"
)

// runHealthCheck performs a GET /healthz against the configured host:port and
// exits with code 0 on success, 1 otherwise. Intended for use as a Docker
// HEALTHCHECK or container liveness probe so we don't need wget/curl in the
// runtime image.
//
// CLI:
//
//	knowledge-worker-agent health [--host=H] [--port=P] [--timeout=Ns] [--url=URL]
//
// Defaults come from KWA_HOST / KWA_PORT env vars (same
// defaults as the server). When --url is provided it overrides host/port.
func runHealthCheck(args []string) int {
	host := envOr("KWA_HOST", "127.0.0.1")
	if host == "0.0.0.0" || host == "" {
		host = "127.0.0.1"
	}
	port := 8765
	if p, err := strconv.Atoi(env.Get("KWA_PORT")); err == nil && p > 0 {
		port = p
	}
	timeout := 3 * time.Second
	url := ""

	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--host="):
			host = strings.TrimPrefix(a, "--host=")
		case strings.HasPrefix(a, "--port="):
			if p, err := strconv.Atoi(strings.TrimPrefix(a, "--port=")); err == nil && p > 0 {
				port = p
			}
		case strings.HasPrefix(a, "--timeout="):
			if d, err := time.ParseDuration(strings.TrimPrefix(a, "--timeout=")); err == nil {
				timeout = d
			}
		case strings.HasPrefix(a, "--url="):
			url = strings.TrimPrefix(a, "--url=")
		case a == "-h" || a == "--help":
			fmt.Println("Usage: knowledge-worker-agent health [--host=H] [--port=P] [--timeout=3s] [--url=URL]")
			fmt.Println("Hits GET /healthz and exits 0 on success, 1 on failure.")
			return 0
		}
	}

	if url == "" {
		url = fmt.Sprintf("http://%s:%d/healthz", host, port)
	}

	// Retry for at least 10 seconds before reporting failure.
	retryDeadline := 10 * time.Second
	retryInterval := 1 * time.Second
	start := time.Now()
	client := &http.Client{Timeout: timeout}

	var lastErr error
	for {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			cancel()
			fmt.Fprintf(os.Stderr, "health: bad URL %q: %v\n", url, err)
			return 1
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			if time.Since(start) >= retryDeadline {
				fmt.Fprintf(os.Stderr, "health: %v (retried for %s)\n", lastErr, time.Since(start).Round(time.Second))
				return 1
			}
			time.Sleep(retryInterval)
			continue
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		resp.Body.Close()
		cancel()
		bodyTrim := strings.TrimSpace(string(body))

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			fmt.Printf("ok %s -> %d %s\n", url, resp.StatusCode, bodyTrim)
			return 0
		}

		lastErr = fmt.Errorf("%s -> %d %s", url, resp.StatusCode, bodyTrim)
		if time.Since(start) >= retryDeadline {
			fmt.Fprintf(os.Stderr, "health: %v (retried for %s)\n", lastErr, time.Since(start).Round(time.Second))
			return 1
		}
		time.Sleep(retryInterval)
	}
}

func envOr(key, fallback string) string {
	if v := env.Get(key); v != "" {
		return v
	}
	return fallback
}
