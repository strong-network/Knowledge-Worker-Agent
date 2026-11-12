// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package telemetry sends OpenTelemetry data about model calls to a collector
// a provider artifact names (kwa.telemetry). The records follow Claude Code's
// names and attributes, so they land in dashboards built for Claude Code.
package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Attr is one attribute. Value is a string, bool, int, int64 or float64.
type Attr struct {
	Key   string
	Value any
}

// Config is a provider's kwa.telemetry block, validated.
type Config struct {
	Endpoint              string
	Interval              time.Duration
	LogUserPrompts        bool
	LogAssistantResponses bool
	LogToolDetails        bool
	LogToolContent        bool
	// Resource holds the configured resource attributes, resolved.
	Resource []Attr
}

// RawConfig is kwa.telemetry as the artifact writes it.
type RawConfig struct {
	Endpoint              string            `json:"endpoint"`
	ExportIntervalSeconds int               `json:"exportIntervalSeconds"`
	LogUserPrompts        bool              `json:"logUserPrompts"`
	LogAssistantResponses bool              `json:"logAssistantResponses"`
	LogToolDetails        bool              `json:"logToolDetails"`
	LogToolContent        bool              `json:"logToolContent"`
	ResourceAttributes    map[string]string `json:"resourceAttributes"`
}

// allowedEnv lists the variables a resource attribute may read: an artifact
// must not be able to send any other value of the environment, such as a
// token, to a collector.
var allowedEnv = map[string]bool{
	"OWNER_EMAIL": true, "OWNER_FULL_NAME": true,
	"GOOGLE_VERTEX_PROJECT": true, "GOOGLE_VERTEX_LOCATION": true,
	"KWA_PROJECT_ID": true, "STRONG_NETWORK_WORKSPACE_ID": true,
}

var (
	attrKeyRe = regexp.MustCompile(`^[a-z0-9_.]+$`)
	envRefRe  = regexp.MustCompile(`^\{env:([A-Z0-9_]+)\}$`)
)

// Parse validates a kwa.telemetry block. The warnings name what was missing
// but usable, such as an attribute whose variable is empty.
func Parse(raw RawConfig) (Config, []string, error) {
	u, err := url.Parse(strings.TrimSpace(raw.Endpoint))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Config{}, nil, fmt.Errorf("endpoint must be an https:// address")
	}
	interval := 60 * time.Second
	if raw.ExportIntervalSeconds != 0 {
		if raw.ExportIntervalSeconds < 5 || raw.ExportIntervalSeconds > 600 {
			return Config{}, nil, fmt.Errorf("exportIntervalSeconds must be between 5 and 600")
		}
		interval = time.Duration(raw.ExportIntervalSeconds) * time.Second
	}
	var warnings []string
	var res []Attr
	for _, k := range sortedKeys(raw.ResourceAttributes) {
		v := raw.ResourceAttributes[k]
		if !attrKeyRe.MatchString(k) {
			return Config{}, nil, fmt.Errorf("resource attribute %q: names are lowercase letters, digits, '_' and '.'", k)
		}
		if strings.Contains(v, "{env:") {
			m := envRefRe.FindStringSubmatch(v)
			if m == nil || !allowedEnv[m[1]] {
				return Config{}, nil, fmt.Errorf("resource attribute %q: only {env:NAME} with one of %s", k, strings.Join(sortedKeys(allowedEnv), ", "))
			}
			v = strings.TrimSpace(os.Getenv(m[1]))
			if v == "" {
				warnings = append(warnings, fmt.Sprintf("resource attribute %q is empty: %s isn't set", k, m[1]))
			}
		}
		res = append(res, Attr{k, v})
	}
	return Config{
		Endpoint:              strings.TrimRight(u.String(), "/"),
		Interval:              interval,
		LogUserPrompts:        raw.LogUserPrompts,
		LogAssistantResponses: raw.LogAssistantResponses,
		LogToolDetails:        raw.LogToolDetails,
		LogToolContent:        raw.LogToolContent,
		Resource:              res,
	}, warnings, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Version is KWA's version, for service.version and the scopes. Set by main.
var Version = "dev"

const (
	serviceName   = "knowledge-worker-agent"
	scopeMetrics  = "com.anthropic.claude_code"
	scopeEvents   = "com.anthropic.claude_code.events"
	scopeTracing  = "com.anthropic.claude_code.tracing"
	maxValueBytes = 64 << 10
	maxRecords    = 10000
	maxQueueBytes = 16 << 20
	retries       = 2
	stopTimeout   = 5 * time.Second
	// maxSendTime caps one export, which must also end within the interval.
	maxSendTime = 30 * time.Second
)

// Status is an exporter's state, for Diagnostics.
type Status struct {
	Endpoint    string    `json:"endpoint"`
	Providers   []string  `json:"providers"`
	LastAttempt time.Time `json:"lastAttempt,omitzero"`
	LastSuccess time.Time `json:"lastSuccess,omitzero"`
	LastError   string    `json:"lastError,omitempty"`
	Queued      int       `json:"queued"`
	Dropped     int       `json:"dropped"`
}

type metricKey struct {
	name, unit, attrs string
}

type metricPoint struct {
	name, unit string
	attrs      []Attr
	value      float64
}

type logRecord struct {
	time            time.Time
	name            string
	attrs           []Attr
	traceID, spanID []byte
	size            int
}

type spanEvent struct {
	time  time.Time
	name  string
	attrs []Attr
}

type span struct {
	traceID, spanID, parentID []byte
	name                      string
	start, end                time.Time
	attrs                     []Attr
	events                    []spanEvent
	failed                    bool
	size                      int
}

// Exporter batches records for one collector.
type Exporter struct {
	cfg      Config
	resource []Attr
	client   *http.Client

	mu         sync.Mutex
	metrics    map[metricKey]*metricPoint
	since      time.Time
	logs       []logRecord
	spans      []span
	queueBytes int
	status     Status

	// ctx ends when the exporter stops, cancelling a send in progress.
	ctx    context.Context
	cancel context.CancelFunc
	stop   chan struct{}
	done   chan struct{}
}

// httpClient is replaced in tests.
var httpClient = &http.Client{Timeout: 30 * time.Second}

func newExporter(cfg Config) *Exporter {
	e := &Exporter{
		cfg:     cfg,
		client:  httpClient,
		metrics: map[metricKey]*metricPoint{},
		since:   time.Now(),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		status:  Status{Endpoint: cfg.Endpoint},
	}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	e.resource = append([]Attr{
		{"service.name", serviceName},
		{"service.version", Version},
		{"host.arch", runtime.GOARCH},
		{"os.type", runtime.GOOS},
		{"os.version", osVersion()},
	}, cfg.Resource...)
	go e.loop()
	return e
}

func osVersion() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Common returns the attributes Claude Code puts on every record: the
// resource attributes it was configured with, the user and the session.
func (e *Exporter) common(sessionID string) []Attr {
	out := make([]Attr, 0, len(e.cfg.Resource)+3)
	out = append(out, e.cfg.Resource...)
	out = append(out, Attr{"user.id", userID()})
	if sessionID != "" {
		out = append(out, Attr{"session.id", sessionID})
	}
	out = append(out, Attr{"terminal.type", serviceName})
	return out
}

var userIDOnce = sync.OnceValue(func() string {
	sum := sha256.Sum256([]byte(os.Getenv("STRONG_NETWORK_WORKSPACE_ID")))
	return hex.EncodeToString(sum[:])
})

func userID() string { return userIDOnce() }

// add sums a metric value into the current interval.
func (e *Exporter) add(name, unit string, attrs []Attr, v float64) {
	key := metricKey{name, unit, attrString(attrs)}
	e.mu.Lock()
	defer e.mu.Unlock()
	if p, ok := e.metrics[key]; ok {
		p.value += v
		return
	}
	e.metrics[key] = &metricPoint{name: name, unit: unit, attrs: attrs, value: v}
}

func attrString(attrs []Attr) string {
	var b strings.Builder
	for _, a := range attrs {
		fmt.Fprintf(&b, "%s=%v\x00", a.Key, a.Value)
	}
	return b.String()
}

func (e *Exporter) event(r logRecord) {
	r.attrs = truncateAll(r.attrs)
	r.size = sizeOf(r.attrs)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.logs = append(e.logs, r)
	e.queueBytes += r.size
	e.trimLocked()
}

func (e *Exporter) span(s span) {
	s.attrs = truncateAll(s.attrs)
	s.size = sizeOf(s.attrs)
	for i := range s.events {
		s.events[i].attrs = truncateAll(s.events[i].attrs)
		s.size += sizeOf(s.events[i].attrs)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans = append(e.spans, s)
	e.queueBytes += s.size
	e.trimLocked()
}

// trimLocked drops the oldest records past the queue's limits.
func (e *Exporter) trimLocked() {
	for len(e.logs)+len(e.spans) > maxRecords || e.queueBytes > maxQueueBytes {
		if len(e.logs) > 0 && (len(e.spans) == 0 || !e.logs[0].time.After(e.spans[0].start)) {
			e.queueBytes -= e.logs[0].size
			e.logs = e.logs[1:]
		} else if len(e.spans) > 0 {
			e.queueBytes -= e.spans[0].size
			e.spans = e.spans[1:]
		} else {
			return
		}
		e.status.Dropped++
	}
}

func truncateAll(attrs []Attr) []Attr {
	for i, a := range attrs {
		if s, ok := a.Value.(string); ok && len(s) > maxValueBytes {
			attrs[i].Value = truncate(s)
		}
	}
	return attrs
}

func truncate(s string) string {
	const marker = "…[truncated]"
	cut := maxValueBytes - len(marker)
	for cut > 0 && cut < len(s) && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + marker
}

func sizeOf(attrs []Attr) int {
	n := 0
	for _, a := range attrs {
		n += len(a.Key) + 16
		if s, ok := a.Value.(string); ok {
			n += len(s)
		}
	}
	return n
}

func (e *Exporter) loop() {
	defer close(e.done)
	t := time.NewTicker(e.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			ctx, cancel := context.WithTimeout(e.ctx, min(e.cfg.Interval, maxSendTime))
			e.flush(ctx)
			cancel()
		case <-e.stop:
			ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
			e.export(ctx, false)
			cancel()
			return
		}
	}
}

// close cancels a send in progress, sends what is queued within stopTimeout,
// and stops the exporter.
func (e *Exporter) close() {
	close(e.stop)
	e.cancel()
	<-e.done
}

// flush exports everything recorded since the last export. What a stop
// interrupts goes back in the queue for the final send.
func (e *Exporter) flush(ctx context.Context) { e.export(ctx, true) }

func (e *Exporter) export(ctx context.Context, keepOnStop bool) {
	e.mu.Lock()
	now := time.Now()
	points := make([]*metricPoint, 0, len(e.metrics))
	for _, p := range e.metrics {
		points = append(points, p)
	}
	since := e.since
	e.metrics, e.since = map[metricKey]*metricPoint{}, now
	logs, spans := e.logs, e.spans
	e.logs, e.spans, e.queueBytes = nil, nil, 0
	e.mu.Unlock()

	var errs []string
	interrupted := false
	failed := func(err error, n int, requeue func()) {
		if keepOnStop && e.ctx.Err() != nil {
			interrupted = true
			requeue()
			return
		}
		errs = append(errs, err.Error())
		e.countDropped(n)
	}
	if len(points) > 0 {
		sort.Slice(points, func(i, j int) bool {
			if points[i].name != points[j].name {
				return points[i].name < points[j].name
			}
			return attrString(points[i].attrs) < attrString(points[j].attrs)
		})
		if err := e.post(ctx, "/v1/metrics", e.encodeMetrics(points, since, now)); err != nil {
			failed(err, len(points), func() { e.requeueMetrics(points, since) })
		}
	}
	if len(logs) > 0 {
		if err := e.post(ctx, "/v1/logs", e.encodeLogs(logs)); err != nil {
			failed(err, len(logs), func() { e.requeueRecords(logs, nil) })
		}
	}
	if len(spans) > 0 {
		if err := e.post(ctx, "/v1/traces", e.encodeSpans(spans)); err != nil {
			failed(err, len(spans), func() { e.requeueRecords(nil, spans) })
		}
	}
	if interrupted || len(points)+len(logs)+len(spans) == 0 {
		return
	}
	e.mu.Lock()
	e.status.LastAttempt = now
	e.status.LastError = strings.Join(errs, "; ")
	if len(errs) == 0 {
		e.status.LastSuccess = now
	}
	e.mu.Unlock()
	if len(errs) > 0 {
		log.Printf("[telemetry] export to %s: %s", e.cfg.Endpoint, strings.Join(errs, "; "))
	}
}

// requeueMetrics adds points back into the current interval, which then
// starts where theirs did.
func (e *Exporter) requeueMetrics(points []*metricPoint, since time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range points {
		key := metricKey{p.name, p.unit, attrString(p.attrs)}
		if q, ok := e.metrics[key]; ok {
			q.value += p.value
		} else {
			e.metrics[key] = p
		}
	}
	e.since = since
}

// requeueRecords puts records back at the front of the queue.
func (e *Exporter) requeueRecords(logs []logRecord, spans []span) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range logs {
		e.queueBytes += r.size
	}
	for _, s := range spans {
		e.queueBytes += s.size
	}
	e.logs = append(logs, e.logs...)
	e.spans = append(spans, e.spans...)
	e.trimLocked()
}

func (e *Exporter) countDropped(n int) {
	e.mu.Lock()
	e.status.Dropped += n
	e.mu.Unlock()
}

// post sends one export request, retrying a 429 or 5xx twice.
func (e *Exporter) post(ctx context.Context, path string, body []byte) error {
	var last error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt) * backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.cfg.Endpoint+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-protobuf")
		resp, err := e.client.Do(req)
		if err != nil {
			last = fmt.Errorf("%s: %v", path, err)
			continue
		}
		resp.Body.Close()
		switch {
		case resp.StatusCode < 300:
			return nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			last = fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
		default:
			return fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
		}
	}
	return last
}

// backoff is the wait before the first retry; replaced in tests.
var backoff = 2 * time.Second

func (e *Exporter) statusSnapshot() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.status
	s.Queued = len(e.logs) + len(e.spans)
	return s
}

func newID(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// --- registry: one exporter per collector, the configuration per provider ---

var (
	regMu     sync.Mutex
	exporters = map[string]*Exporter{}
	byProv    = map[string]*Exporter{}
)

// Configure replaces the telemetry configuration: one Config per provider id.
// Exporters for collectors no longer named send what they hold and stop.
func Configure(cfgs map[string]Config) {
	regMu.Lock()
	keep := map[string]*Exporter{}
	prov := map[string]*Exporter{}
	var stale []*Exporter
	for _, id := range sortedKeys(cfgs) {
		cfg := cfgs[id]
		key := exporterKey(cfg)
		e := keep[key]
		if e == nil {
			if old := exporters[key]; old != nil {
				e = old
			} else {
				e = newExporter(cfg)
				log.Printf("[telemetry] %s: sending to %s every %s", id, cfg.Endpoint, cfg.Interval)
			}
			keep[key] = e
		}
		prov[id] = e
	}
	for key, e := range exporters {
		if keep[key] == nil {
			stale = append(stale, e)
		}
	}
	exporters, byProv = keep, prov
	regMu.Unlock()
	// In parallel, so a refresh or shutdown waits stopTimeout once, not per collector.
	var wg sync.WaitGroup
	for _, e := range stale {
		wg.Go(e.close)
	}
	wg.Wait()
}

// exporterKey is the configuration as text, so two providers with the same
// collector and settings share one exporter.
func exporterKey(c Config) string {
	return fmt.Sprintf("%s|%s|%v%v%v%v|%s", c.Endpoint, c.Interval, c.LogUserPrompts,
		c.LogAssistantResponses, c.LogToolDetails, c.LogToolContent, attrString(c.Resource))
}

// For returns the exporter for a provider, or nil.
func For(providerID string) *Exporter {
	regMu.Lock()
	defer regMu.Unlock()
	return byProv[providerID]
}

// ConfigFor returns a provider's configuration, if it has one.
func ConfigFor(providerID string) (Config, bool) {
	if e := For(providerID); e != nil {
		return e.cfg, true
	}
	return Config{}, false
}

// Statuses reports every exporter, for Diagnostics.
func Statuses() []Status {
	regMu.Lock()
	list := make([]*Exporter, 0, len(exporters))
	for _, e := range exporters {
		list = append(list, e)
	}
	users := map[*Exporter][]string{}
	for id, e := range byProv {
		users[e] = append(users[e], id)
	}
	regMu.Unlock()
	out := make([]Status, 0, len(list))
	for _, e := range list {
		s := e.statusSnapshot()
		s.Providers = users[e]
		sort.Strings(s.Providers)
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Endpoint < out[j].Endpoint })
	return out
}

// Shutdown sends what every exporter holds and stops them.
func Shutdown() { Configure(nil) }
