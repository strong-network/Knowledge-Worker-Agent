// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// collector is a fake OTLP collector that keeps each request body by path.
type collector struct {
	mu       sync.Mutex
	bodies   map[string][][]byte
	types    []string
	statuses []int // answered in turn, then 200
}

func newCollector(t *testing.T) (*collector, *httptest.Server) {
	c := &collector{bodies: map[string][][]byte{}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.types = append(c.types, r.Header.Get("Content-Type"))
		status := http.StatusOK
		if len(c.statuses) > 0 {
			status, c.statuses = c.statuses[0], c.statuses[1:]
		}
		if status == http.StatusOK {
			c.bodies[r.URL.Path] = append(c.bodies[r.URL.Path], b)
		}
		c.mu.Unlock()
		w.WriteHeader(status)
	}))
	useServer(t, srv)
	return c, srv
}

// useServer sends exports to srv for the test, with retries that don't wait.
func useServer(t *testing.T, srv *httptest.Server) {
	prev, prevBackoff := httpClient, backoff
	httpClient, backoff = srv.Client(), time.Millisecond
	t.Cleanup(func() {
		Shutdown()
		srv.Close()
		httpClient, backoff = prev, prevBackoff
	})
}

// hangingServer answers nothing until the client gives up, except that
// answer(n) can let request n (from 0) through.
func hangingServer(t *testing.T, answer func(n int) bool) (*httptest.Server, chan string) {
	var mu sync.Mutex
	n := 0
	seen := make(chan string, 16)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		ok := answer(n)
		n++
		mu.Unlock()
		if !ok {
			seen <- "hung " + r.URL.Path
			<-r.Context().Done()
			return
		}
		seen <- r.URL.Path
	}))
	useServer(t, srv)
	return srv, seen
}

func next(t *testing.T, seen chan string) string {
	t.Helper()
	select {
	case s := <-seen:
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("no request reached the collector")
		return ""
	}
}

func (c *collector) got(path string) [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bodies[path]
}

func testConfig(endpoint string) Config {
	return Config{
		Endpoint: endpoint, Interval: time.Hour,
		LogUserPrompts: true, LogAssistantResponses: true, LogToolDetails: true, LogToolContent: true,
		Resource: []Attr{{"user.email", "alex@example.com"}, {"gcp.project.id", "proj"}},
	}
}

func TestParse(t *testing.T) {
	t.Setenv("OWNER_EMAIL", "alex@example.com")
	t.Setenv("GOOGLE_VERTEX_PROJECT", "")
	cfg, warnings, err := Parse(RawConfig{
		Endpoint: "https://otel.example.com/", ExportIntervalSeconds: 30, LogUserPrompts: true,
		ResourceAttributes: map[string]string{"user.email": "{env:OWNER_EMAIL}", "gcp.project.id": "{env:GOOGLE_VERTEX_PROJECT}", "team": "docs"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "https://otel.example.com" || cfg.Interval != 30*time.Second || !cfg.LogUserPrompts || cfg.LogToolContent {
		t.Errorf("cfg = %+v", cfg)
	}
	want := []Attr{{"gcp.project.id", ""}, {"team", "docs"}, {"user.email", "alex@example.com"}}
	if len(cfg.Resource) != 3 || cfg.Resource[0] != want[0] || cfg.Resource[1] != want[1] || cfg.Resource[2] != want[2] {
		t.Errorf("resource = %v, want %v", cfg.Resource, want)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "GOOGLE_VERTEX_PROJECT") {
		t.Errorf("warnings = %v", warnings)
	}
	if d, _, _ := Parse(RawConfig{Endpoint: "https://o.example.com"}); d.Interval != time.Minute {
		t.Errorf("default interval = %s", d.Interval)
	}

	for name, raw := range map[string]RawConfig{
		"http":            {Endpoint: "http://otel.example.com"},
		"no host":         {Endpoint: "https://"},
		"query":           {Endpoint: "https://otel.example.com?x=1"},
		"short interval":  {Endpoint: "https://o.example.com", ExportIntervalSeconds: 4},
		"long interval":   {Endpoint: "https://o.example.com", ExportIntervalSeconds: 601},
		"bad key":         {Endpoint: "https://o.example.com", ResourceAttributes: map[string]string{"User Email": "x"}},
		"secret variable": {Endpoint: "https://o.example.com", ResourceAttributes: map[string]string{"t": "{env:GITHUB_TOKEN}"}},
		"embedded ref":    {Endpoint: "https://o.example.com", ResourceAttributes: map[string]string{"t": "x-{env:OWNER_EMAIL}"}},
	} {
		if _, _, err := Parse(raw); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMetricsAreDeltaSumsInProtobuf(t *testing.T) {
	col, srv := newCollector(t)
	Configure(map[string]Config{"gw": testConfig(srv.URL)})
	e := For("gw")
	e.add("claude_code.token.usage", "tokens", []Attr{{"type", "input"}}, 2)
	e.add("claude_code.token.usage", "tokens", []Attr{{"type", "input"}}, 3)
	e.add("claude_code.token.usage", "tokens", []Attr{{"type", "output"}}, 7)
	e.add("claude_code.cost.usage", "USD", nil, 0.5)
	e.flush(t.Context())

	bodies := col.got("/v1/metrics")
	if len(bodies) != 1 {
		t.Fatalf("got %d metric requests", len(bodies))
	}
	if col.types[0] != "application/x-protobuf" {
		t.Errorf("content type = %q", col.types[0])
	}
	rm := msgs(bodies[0], reqResourceMetrics)[0]
	res := attrMap(raw(rm, rmResource), resourceAttrs)
	if res["service.name"] != "knowledge-worker-agent" || res["user.email"] != "alex@example.com" || res["gcp.project.id"] != "proj" {
		t.Errorf("resource = %v", res)
	}
	sm := msgs(encodeOf(rm), rmScopeMetrics)[0]
	if s := msgs(encodeOf(sm), smScope)[0]; str(s, scopeName) != scopeMetrics {
		t.Errorf("scope = %q", str(s, scopeName))
	}
	got := map[string]float64{}
	for _, m := range msgs(encodeOf(sm), smMetrics) {
		sum := msgs(encodeOf(m), metricSum)[0]
		if num(sum, sumTemporality) != temporalityDelta || num(sum, sumMonotonic) != 1 {
			t.Errorf("%s: not a monotonic delta sum", str(m, metricName))
		}
		for _, dp := range msgs(encodeOf(sum), sumDataPoints) {
			a := attrMap(encodeOf(dp), dpAttributes)
			got[str(m, metricName)+"/"+strOf(a["type"])] = dbl(dp, dpAsDouble)
			if num(dp, dpStartTime) == 0 || num(dp, dpTime) <= num(dp, dpStartTime) {
				t.Errorf("bad interval on %s", str(m, metricName))
			}
		}
	}
	want := map[string]float64{"claude_code.token.usage/input": 5, "claude_code.token.usage/output": 7, "claude_code.cost.usage/": 0.5}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v (all: %v)", k, got[k], v, got)
		}
	}

	// Delta: the next export starts from zero.
	e.flush(t.Context())
	if n := len(col.got("/v1/metrics")); n != 1 {
		t.Errorf("an empty interval sent %d more requests", n-1)
	}
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}

func TestRetriesAndDrops(t *testing.T) {
	col, srv := newCollector(t)
	Configure(map[string]Config{"gw": testConfig(srv.URL)})
	e := For("gw")

	col.statuses = []int{503, 429} // two retries, then accepted
	e.event(logRecord{time: time.Now(), name: "user_prompt"})
	e.flush(t.Context())
	if len(col.got("/v1/logs")) != 1 || e.statusSnapshot().LastError != "" || e.statusSnapshot().LastSuccess.IsZero() {
		t.Errorf("a 503 then a 429 weren't retried: %+v", e.statusSnapshot())
	}
	sent := e.statusSnapshot().LastSuccess

	col.statuses = []int{400}
	e.event(logRecord{time: time.Now(), name: "user_prompt"})
	e.flush(t.Context())
	st := e.statusSnapshot()
	if len(col.got("/v1/logs")) != 1 || st.Dropped != 1 || !strings.Contains(st.LastError, "HTTP 400") {
		t.Errorf("a 400 was retried or not counted: %+v", st)
	}
	if st.LastSuccess != sent || !st.LastAttempt.After(sent) {
		t.Errorf("a failed attempt moved the last success: %+v", st)
	}
}

func TestASendHasADeadline(t *testing.T) {
	srv, seen := hangingServer(t, func(int) bool { return false })
	cfg := testConfig(srv.URL)
	cfg.Interval = 50 * time.Millisecond
	Configure(map[string]Config{"gw": cfg})
	e := For("gw")
	e.add("claude_code.cost.usage", "USD", nil, 1)
	e.event(logRecord{time: time.Now(), name: "user_prompt"})
	next(t, seen)

	deadline := time.Now().Add(3 * time.Second)
	for e.statusSnapshot().LastAttempt.IsZero() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	st := e.statusSnapshot()
	if st.LastAttempt.IsZero() || st.LastError == "" || !st.LastSuccess.IsZero() {
		t.Fatalf("a send to a collector that never answers didn't end: %+v", st)
	}
	if st.Dropped != 2 {
		t.Errorf("dropped %d, want the metric and the event", st.Dropped)
	}
}

func TestStopCancelsASendInProgress(t *testing.T) {
	srv, seen := hangingServer(t, func(n int) bool { return n > 0 })
	cfg := testConfig(srv.URL)
	cfg.Interval = time.Second // a send's deadline, too: longer than the test waits
	Configure(map[string]Config{"gw": cfg})
	For("gw").event(logRecord{time: time.Now(), name: "user_prompt"})
	if got := next(t, seen); got != "hung /v1/logs" {
		t.Fatalf("first request = %q", got)
	}

	start := time.Now()
	Shutdown()
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("shutdown waited %s for a send in progress", d)
	}
	if got := next(t, seen); got != "/v1/logs" {
		t.Errorf("the interrupted event wasn't sent at shutdown: %q", got)
	}
}

func TestQueueLimitAndTruncation(t *testing.T) {
	_, srv := newCollector(t)
	Configure(map[string]Config{"gw": testConfig(srv.URL)})
	e := For("gw")
	for i := 0; i < maxRecords+5; i++ {
		e.event(logRecord{time: time.Now(), name: "x"})
	}
	if st := e.statusSnapshot(); st.Queued != maxRecords || st.Dropped != 5 {
		t.Errorf("queued %d, dropped %d", st.Queued, st.Dropped)
	}
	long := strings.Repeat("é", maxValueBytes)
	got := truncate(long)
	if len(got) > maxValueBytes || !strings.HasSuffix(got, "…[truncated]") || !strings.HasPrefix(got, "é") {
		t.Errorf("truncate: %d bytes, suffix ok %v", len(got), strings.HasSuffix(got, "…[truncated]"))
	}
	for i := 0; i < len(got)-len("…[truncated]"); i += 2 {
		if got[i:i+2] != "é" {
			t.Fatalf("cut inside a character at %d", i)
		}
	}
}

func TestShutdownSendsWhatIsQueued(t *testing.T) {
	col, srv := newCollector(t)
	Configure(map[string]Config{"gw": testConfig(srv.URL)})
	For("gw").event(logRecord{time: time.Now(), name: "user_prompt"})
	Shutdown()
	if len(col.got("/v1/logs")) != 1 {
		t.Error("queued events weren't sent at shutdown")
	}
	if For("gw") != nil {
		t.Error("an exporter is left after shutdown")
	}
}

func TestProvidersShareACollector(t *testing.T) {
	_, srv := newCollector(t)
	cfg := testConfig(srv.URL)
	Configure(map[string]Config{"a": cfg, "b": cfg})
	if For("a") == nil || For("a") != For("b") {
		t.Error("two providers with the same settings got two exporters")
	}
	if s := Statuses(); len(s) != 1 || len(s[0].Providers) != 2 {
		t.Errorf("statuses = %+v", s)
	}
	Configure(map[string]Config{"a": cfg})
	if For("b") != nil || For("a") == nil {
		t.Error("removing a provider didn't take its telemetry away")
	}
}
