// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

const gatewayConfig = `{"provider":{"gw":{
	"options":{"baseURL":"{env:KWA_ENDPOINT_GW}/v1/models","headers":{"x-version":"1","x-region":"{env:KWA_ENDPOINT_GW}"}},
	"whitelist":["a"]}}}`

const gatewayMeta = `{"providers":[{"id":"gw","endpoints":[
	{"id":"na","label":"US","url":"https://na.example.com"},
	{"id":"eu","label":"Europe","url":"https://eu.example.com/"},
	{"id":"ap","url":"https://ap.example.com"}]}]}`

// withEndpointState gives a test a clean endpoint state, scripted latencies
// (a missing url fails), an in-memory preference store, and a change counter.
func withEndpointState(t *testing.T, latency map[string]time.Duration) (prefs map[string]string, changes *int) {
	t.Helper()
	t.Setenv(EndpointEnvVar("gw"), "")
	os.Unsetenv(EndpointEnvVar("gw"))

	epMu.Lock()
	epMeasured, epActive, epAuto = map[string]map[string]measurement{}, map[string]string{}, map[string]bool{}
	epMu.Unlock()

	prevMeasure := measureFn
	measureFn = func(_ context.Context, u string) (time.Duration, error) {
		if d, ok := latency[u]; ok {
			return d, nil
		}
		return 0, errors.New("timeout")
	}
	prefs = map[string]string{}
	SetEndpointStore(
		func(k string) (string, bool) { v, ok := prefs[k]; return v, ok },
		func(k, v string) error { prefs[k] = v; return nil })
	changes = new(int)
	SetEndpointChangeHook(func() { *changes++ })
	t.Cleanup(func() {
		measureFn = prevMeasure
		SetEndpointStore(nil, nil)
		SetEndpointChangeHook(nil)
	})
	return prefs, changes
}

func TestEndpointEnvVar(t *testing.T) {
	for id, want := range map[string]string{
		"google-vertex": "KWA_ENDPOINT_GOOGLE_VERTEX",
		"onprem":        "KWA_ENDPOINT_ONPREM",
		"a.b/c":         "KWA_ENDPOINT_A_B_C",
	} {
		if got := EndpointEnvVar(id); got != want {
			t.Errorf("EndpointEnvVar(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestValidEndpointsKeepsOnlyHTTPSOrigins(t *testing.T) {
	got := validEndpoints([]Endpoint{
		{ID: "ok", URL: "https://ok.example.com/"},
		{ID: "port", Label: "Port", URL: "https://port.example.com:8443"},
		{ID: "http", URL: "http://plain.example.com"},
		{ID: "path", URL: "https://path.example.com/v1"},
		{ID: "user", URL: "https://me@user.example.com"},
		{ID: "query", URL: "https://q.example.com?x=1"},
		{ID: "ok", URL: "https://duplicate.example.com"},
		{ID: EndpointAuto, URL: "https://auto.example.com"},
		{ID: "", URL: "https://noid.example.com"},
	})
	want := []Endpoint{
		{ID: "ok", Label: "ok", URL: "https://ok.example.com"},
		{ID: "port", Label: "Port", URL: "https://port.example.com:8443"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestApplyEndpointsExportsTheFastest(t *testing.T) {
	centralDir(t, gatewayConfig, gatewayMeta)
	_, changes := withEndpointState(t, map[string]time.Duration{
		"https://na.example.com": 240 * time.Millisecond,
		"https://ap.example.com": 50 * time.Millisecond,
	})

	ApplyEndpoints(context.Background())

	if got := os.Getenv("KWA_ENDPOINT_GW"); got != "https://ap.example.com" {
		t.Fatalf("KWA_ENDPOINT_GW = %q, want the fastest endpoint", got)
	}
	if *changes != 1 {
		t.Errorf("change hook fired %d times, want 1", *changes)
	}

	p := withEndpoints(Provider{ID: "gw"}, endpointConfig())
	if p.Endpoint != "ap" || !p.EndpointAuto {
		t.Errorf("endpoint = %q auto=%v, want ap/auto", p.Endpoint, p.EndpointAuto)
	}
	if len(p.Endpoints) != 3 {
		t.Fatalf("endpoints = %+v", p.Endpoints)
	}
	na, eu := p.Endpoints[0], p.Endpoints[1]
	if !na.Reachable || na.LatencyMs != 240 || na.Label != "US" {
		t.Errorf("na = %+v", na)
	}
	if !eu.Measured || eu.Reachable {
		t.Errorf("eu = %+v, want measured and unreachable", eu)
	}

	// Nothing changed, so nothing restarts.
	ApplyEndpoints(context.Background())
	if *changes != 1 {
		t.Errorf("change hook fired again for the same endpoint")
	}
}

func TestApplyEndpointsWhenNothingAnswers(t *testing.T) {
	centralDir(t, gatewayConfig, gatewayMeta)
	withEndpointState(t, nil)

	ApplyEndpoints(context.Background())

	// The variable must hold an address, or opencode's base URL is a bare path.
	if got := os.Getenv("KWA_ENDPOINT_GW"); got != "https://na.example.com" {
		t.Errorf("KWA_ENDPOINT_GW = %q, want the first endpoint", got)
	}
}

func TestSelectEndpoint(t *testing.T) {
	centralDir(t, gatewayConfig, gatewayMeta)
	prefs, _ := withEndpointState(t, map[string]time.Duration{
		"https://na.example.com": 240 * time.Millisecond,
		"https://eu.example.com": 900 * time.Millisecond,
		"https://ap.example.com": 50 * time.Millisecond,
	})
	ApplyEndpoints(context.Background())

	if err := SelectEndpoint(context.Background(), "gw", "eu"); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("KWA_ENDPOINT_GW"); got != "https://eu.example.com" {
		t.Errorf("after choosing eu: %q", got)
	}
	if prefs["provider_endpoint:gw"] != "eu" {
		t.Errorf("choice not saved: %v", prefs)
	}
	// A restart keeps the user's choice even though it is slower.
	ApplyEndpoints(context.Background())
	if got := os.Getenv("KWA_ENDPOINT_GW"); got != "https://eu.example.com" {
		t.Errorf("choice not kept at startup: %q", got)
	}

	if err := SelectEndpoint(context.Background(), "gw", EndpointAuto); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("KWA_ENDPOINT_GW"); got != "https://ap.example.com" {
		t.Errorf("after auto: %q, want the fastest", got)
	}

	if err := SelectEndpoint(context.Background(), "gw", "https://evil.example.com"); err == nil {
		t.Error("an endpoint outside the configuration was accepted")
	}
	if err := SelectEndpoint(context.Background(), "other", "na"); err == nil {
		t.Error("a provider without endpoints accepted a choice")
	}
}

func TestChoiceOfARemovedEndpointFallsBackToAuto(t *testing.T) {
	centralDir(t, gatewayConfig, gatewayMeta)
	prefs, _ := withEndpointState(t, map[string]time.Duration{"https://ap.example.com": time.Millisecond})
	prefs["provider_endpoint:gw"] = "gone"

	ApplyEndpoints(context.Background())

	if got := os.Getenv("KWA_ENDPOINT_GW"); got != "https://ap.example.com" {
		t.Errorf("KWA_ENDPOINT_GW = %q", got)
	}
}

func TestRequestConfigResolvesPlaceholders(t *testing.T) {
	centralDir(t, gatewayConfig, gatewayMeta)
	t.Setenv("KWA_ENDPOINT_GW", "https://na.example.com")

	base, headers := RequestConfig("gw")
	if base != "https://na.example.com/v1/models" {
		t.Errorf("base = %q", base)
	}
	if headers["x-version"] != "1" || headers["x-region"] != "https://na.example.com" {
		t.Errorf("headers = %v", headers)
	}
	if base, headers := RequestConfig("missing"); base != "" || headers != nil {
		t.Errorf("undeclared provider: %q %v", base, headers)
	}
}

func TestMeasureEndpoint(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("a measurement carried credentials")
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	prev := endpointClient
	endpointClient = srv.Client()
	t.Cleanup(func() { endpointClient = prev })

	if _, err := measureEndpoint(context.Background(), srv.URL); err != nil {
		t.Errorf("a refusal should count as an answer: %v", err)
	}
	srv.Close()
	if _, err := measureEndpoint(context.Background(), srv.URL); err == nil {
		t.Error("a closed server counted as reachable")
	}
}
