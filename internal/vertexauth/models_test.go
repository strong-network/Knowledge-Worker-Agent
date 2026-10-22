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

func TestModelRequest(t *testing.T) {
	maas := "https://${GOOGLE_VERTEX_ENDPOINT}/v1/projects/${GOOGLE_VERTEX_PROJECT}/locations/${GOOGLE_VERTEX_LOCATION}/endpoints/openapi"
	root := "https://aiplatform.googleapis.com/v1/projects/p/locations/global"
	cases := []struct {
		base     string
		c        Candidate
		url      string
		bodyHas  string
		location string
	}{
		{"", Candidate{Model: "claude-x@default", API: APIAnthropic}, root + "/publishers/anthropic/models/claude-x@default:rawPredict", "{}", "global"},
		{"", Candidate{Model: "gemini-x", API: APIGemini}, root + "/publishers/google/models/gemini-x:generateContent", "{}", "global"},
		{"", Candidate{Model: "a/b-maas", API: APIOpenAI, URL: maas},
			"https://us-east5-aiplatform.googleapis.com/v1/projects/p/locations/us-east5/endpoints/openapi/chat/completions",
			`"model":"a/b-maas","messages":[]`, "us-east5"},
		{"https://gw.example.com/v1/x/models/", Candidate{Model: "claude-x@default", API: APIAnthropic}, "https://gw.example.com/v1/x/models/claude-x@default:rawPredict", "{}", "global"},
		{"https://gw.example.com/v1/x/models", Candidate{Model: "gemini-x", API: APIGemini}, "https://gw.example.com/v1/x/models/gemini-x:generateContent", "{}", "global"},
		{"", Candidate{Model: "a/b-maas", API: APIOpenAI}, "", "", "global"},
		{"", Candidate{Model: "m", API: APIUnknown}, "", "", "global"},
	}
	for _, tc := range cases {
		url, body := modelRequest(tc.base, "p", tc.location, tc.c)
		if url != tc.url || !strings.Contains(body, tc.bodyHas) {
			t.Errorf("%+v via %q: got %q %q, want %q containing %q", tc.c, tc.base, url, body, tc.url, tc.bodyHas)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   Availability
	}{
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"messages: Field required"}}`, Available},
		{400, `{"error":{"code":400,"message":"Unable to submit request because at least one contents field is required"}}`, Available},
		{400, `{"error":{"code":400,"message":"Organization Policy constraint constraints/vertexai.allowedModels violated"}}`, Unavailable},
		{403, `{"error":{"message":"Model not permitted","type":"forbidden"}}`, Unavailable},
		{404, `{"error":{"code":404,"message":"Publisher model was not found"}}`, Unavailable},
		{401, `{"error":{"message":"Token invalid or expired - re-authenticate"}}`, Unknown},
		{429, ``, Unknown},
		{503, `<html>Service Unavailable</html>`, Unknown},
		{200, `{}`, Unknown},
	}
	for _, tc := range cases {
		if got := classify(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("classify(%d, %s) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

func TestCheckModelsThroughAGateway(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	restore := swapBin(writeFakeGcloud(t, "#!/bin/sh\necho ya29.fake-token\n"))
	defer restore()
	t.Setenv("GOOGLE_VERTEX_PROJECT", "p")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch {
		case r.Header.Get("Authorization") != "Bearer ya29.fake-token" || r.Header.Get("anthropic-version") == "":
			w.WriteHeader(http.StatusUnauthorized)
		case string(b) != "{}":
			t.Errorf("a check sent a request a model could answer: %s", b)
		case strings.Contains(r.URL.Path, "/served@default:"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error"}}`))
		case strings.Contains(r.URL.Path, "/flaky@default:"):
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()

	got, err := CheckModels(context.Background(),
		Target{BaseURL: srv.URL + "/v1/models", Headers: map[string]string{"anthropic-version": "2023-06-01"}},
		[]Candidate{
			{Model: "served@default", API: APIAnthropic},
			{Model: "refused@default", API: APIAnthropic},
			{Model: "gemini-x", API: APIGemini},
			{Model: "flaky@default", API: APIAnthropic},
			{Model: "mystery", API: APIUnknown},
		})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Availability{
		"served@default": Available, "refused@default": Unavailable, "gemini-x": Unavailable,
		"flaky@default": Unknown, "mystery": Unknown,
	}
	for m, w := range want {
		if got[m] != w {
			t.Errorf("%s = %v, want %v", m, got[m], w)
		}
	}
}

func TestCheckModelsWithoutCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	restore := swapBin(writeFakeGcloud(t, "#!/bin/sh\nexit 1\n"))
	defer restore()
	if _, err := CheckModels(context.Background(), Target{}, []Candidate{{Model: "m", API: APIAnthropic}}); err == nil {
		t.Error("a check without credentials reported results")
	}
}
