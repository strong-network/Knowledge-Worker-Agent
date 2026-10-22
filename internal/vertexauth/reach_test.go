// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package vertexauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestCheckReachableAtAConfiguredTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	restore := swapBin(writeFakeGcloud(t, "#!/bin/sh\necho ya29.fake-token\n"))
	defer restore()

	var gotPath, gotAuth, gotVersion, gotBody string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotVersion = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("anthropic-version")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = w.Write([]byte(`{"error":{"message":"Model not permitted","type":"forbidden"}}`))
		}
	}))
	defer srv.Close()

	target := Target{
		BaseURL: srv.URL + "/v1/projects/p/locations/global/publishers/anthropic/models/",
		Headers: map[string]string{"anthropic-version": "2023-06-01"},
		Model:   "claude-haiku-4-5@20251001",
	}
	if err := CheckReachable(context.Background(), target); err != nil {
		t.Fatalf("CheckReachable: %v", err)
	}
	if gotPath != "/v1/projects/p/locations/global/publishers/anthropic/models/claude-haiku-4-5@20251001:streamRawPredict" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer ya29.fake-token" || gotVersion != "2023-06-01" {
		t.Errorf("headers: auth=%q version=%q", gotAuth, gotVersion)
	}
	if strings.HasPrefix(gotBody, `{"anthropic_version"`) {
		t.Errorf("anthropic_version sent first: %s", gotBody)
	}

	status = http.StatusForbidden
	err := CheckReachable(context.Background(), target)
	if err == nil || err.Error() != "Model not permitted" {
		t.Errorf("refusal = %v, want the gateway's message", err)
	}
}
