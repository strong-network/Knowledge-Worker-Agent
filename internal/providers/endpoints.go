// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Regional endpoints. A provider artifact can list the addresses a provider is
// served at — an AI gateway with one host per region, say — in kwa.endpoints,
// and read the one in use from {env:KWA_ENDPOINT_<ID>} in its own options.
// This file picks that one: the user's choice when they made one, otherwise the
// fastest endpoint this workspace reaches. The user picks by id only, so the
// addresses a credential can be sent to are always the administrator's.

// Endpoint is one entry of a provider artifact's kwa.endpoints list.
type Endpoint struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

// EndpointStatus is an endpoint as the Accounts dialog shows it.
type EndpointStatus struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Measured is false until the endpoint has been tried; Reachable and
	// LatencyMs mean nothing before then.
	Measured  bool  `json:"measured"`
	Reachable bool  `json:"reachable"`
	LatencyMs int64 `json:"latencyMs,omitempty"`
}

// EndpointAuto is the selection that lets the workspace choose.
const EndpointAuto = "auto"

// endpointMeasureTimeout bounds one measurement. Startup waits for it, and an
// endpoint slower than this to answer an empty request is no one's best choice.
var endpointMeasureTimeout = 3 * time.Second

type measurement struct {
	ok      bool
	latency time.Duration
}

var (
	// applyMu serializes the runs that choose endpoints, so a user's choice
	// and a startup pass cannot interleave.
	applyMu sync.Mutex

	epMu       sync.RWMutex
	epMeasured = map[string]map[string]measurement{}
	epActive   = map[string]string{}
	epAuto     = map[string]bool{}

	endpointPrefs struct {
		Get func(key string) (string, bool)
		Set func(key, value string) error
	}
	onEndpointChange func()

	// measureFn is replaced in tests.
	measureFn = measureEndpoint
)

// SetEndpointStore installs where the user's endpoint choices are kept.
func SetEndpointStore(get func(key string) (string, bool), set func(key, value string) error) {
	endpointPrefs.Get, endpointPrefs.Set = get, set
}

// SetEndpointChangeHook installs a callback fired after a provider's endpoint
// changes, for processes that read the environment only when they start.
func SetEndpointChangeHook(fn func()) { onEndpointChange = fn }

// EndpointEnvVar names the variable carrying a provider's endpoint: KWA_ENDPOINT_
// and the id in upper case, with any other character as an underscore.
func EndpointEnvVar(providerID string) string {
	var b strings.Builder
	b.WriteString("KWA_ENDPOINT_")
	for _, r := range strings.ToUpper(providerID) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// validEndpoints keeps the entries with an id not seen before and an https
// origin, returned without a trailing slash so the artifact can append a path.
func validEndpoints(list []Endpoint) []Endpoint {
	seen := map[string]bool{}
	var out []Endpoint
	for _, e := range list {
		id := strings.TrimSpace(e.ID)
		if id == "" || id == EndpointAuto || seen[id] {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(e.URL))
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			log.Printf("[providers] ignoring endpoint %q: the url must be https:// and a host only", id)
			continue
		}
		seen[id] = true
		label := strings.TrimSpace(e.Label)
		if label == "" {
			label = id
		}
		out = append(out, Endpoint{ID: id, Label: label, URL: "https://" + u.Host})
	}
	return out
}

// endpointConfig returns the valid endpoints of every provider the platform
// configuration declares. It doesn't depend on central management being on:
// opencode reads the platform configuration either way, so its placeholders
// need a value either way.
func endpointConfig() map[string][]Endpoint {
	dir := configDirFn()
	if dir == "" {
		return nil
	}
	declared, err := declaredProviderIDs(filepath.Join(dir, "opencode.json"))
	if err != nil {
		return nil
	}
	out := map[string][]Endpoint{}
	for _, m := range readProviderMeta(filepath.Join(dir, "provider-meta.json")) {
		if !declared[m.ID] {
			continue
		}
		if eps := validEndpoints(m.Endpoints); len(eps) > 0 {
			out[m.ID] = eps
		}
	}
	return out
}

// ApplyEndpoints measures every configured endpoint and exports the one each
// provider should use. Run at startup and after central configuration is
// refreshed, before models are discovered.
func ApplyEndpoints(ctx context.Context) {
	applyMu.Lock()
	defer applyMu.Unlock()

	cfg := endpointConfig()
	changed := false
	// Export a choice at once: until the variable is set, the provider's
	// address is a bare path, and measuring takes seconds.
	for id, eps := range cfg {
		changed = activate(id, eps) || changed
	}
	measureAll(ctx, cfg)
	for _, id := range sortedEndpointProviders(cfg) {
		changed = activate(id, cfg[id]) || changed
		logChoice(id, cfg[id])
	}
	if changed && onEndpointChange != nil {
		onEndpointChange()
	}
}

// SelectEndpoint records the user's choice for a provider, an endpoint id or
// EndpointAuto, and applies it.
func SelectEndpoint(ctx context.Context, providerID, endpointID string) error {
	applyMu.Lock()
	defer applyMu.Unlock()

	eps := endpointConfig()[providerID]
	if len(eps) == 0 {
		return fmt.Errorf("this provider has no regional endpoints")
	}
	endpointID = strings.TrimSpace(endpointID)
	if endpointID == "" {
		endpointID = EndpointAuto
	}
	if endpointID != EndpointAuto && !hasEndpoint(eps, endpointID) {
		return fmt.Errorf("unknown endpoint %q", endpointID)
	}
	if endpointPrefs.Set == nil {
		return fmt.Errorf("endpoint choices cannot be saved")
	}
	if err := endpointPrefs.Set(prefKey(providerID), endpointID); err != nil {
		return fmt.Errorf("could not save the choice: %v", err)
	}
	if endpointID == EndpointAuto {
		measureAll(ctx, map[string][]Endpoint{providerID: eps})
	}
	if activate(providerID, eps) {
		logChoice(providerID, eps)
		if onEndpointChange != nil {
			onEndpointChange()
		}
		// The last verdict was for the old address.
		Forget(providerID)
		go Probe(context.Background(), providerID)
	}
	return nil
}

func prefKey(providerID string) string { return "provider_endpoint:" + providerID }

func preference(providerID string) string {
	if endpointPrefs.Get == nil {
		return ""
	}
	v, _ := endpointPrefs.Get(prefKey(providerID))
	return strings.TrimSpace(v)
}

func hasEndpoint(eps []Endpoint, id string) bool {
	for _, e := range eps {
		if e.ID == id {
			return true
		}
	}
	return false
}

// choose returns the endpoint a provider should use, and whether the workspace
// chose it rather than the user. A choice naming an endpoint the configuration
// no longer lists counts as no choice.
func choose(providerID string, eps []Endpoint) (Endpoint, bool) {
	if pref := preference(providerID); pref != "" && pref != EndpointAuto {
		for _, e := range eps {
			if e.ID == pref {
				return e, false
			}
		}
	}
	epMu.RLock()
	m := epMeasured[providerID]
	epMu.RUnlock()

	best, found := Endpoint{}, false
	var bestLatency time.Duration
	for _, e := range eps {
		r, ok := m[e.ID]
		if ok && r.ok && (!found || r.latency < bestLatency) {
			best, bestLatency, found = e, r.latency, true
		}
	}
	if found {
		return best, true
	}
	// Nothing answered, or nothing was measured yet: the first endpoint not
	// known to be unreachable, so a failed measurement doesn't strand the
	// provider on a dead address while another was never tried.
	for _, e := range eps {
		if r, ok := m[e.ID]; !ok || r.ok {
			return e, true
		}
	}
	return eps[0], true
}

// activate exports a provider's endpoint and reports whether it changed.
func activate(providerID string, eps []Endpoint) bool {
	e, auto := choose(providerID, eps)
	name := EndpointEnvVar(providerID)
	changed := os.Getenv(name) != e.URL
	if changed {
		if err := os.Setenv(name, e.URL); err != nil {
			log.Printf("[providers] could not set %s: %v", name, err)
			return false
		}
	}
	epMu.Lock()
	epActive[providerID], epAuto[providerID] = e.ID, auto
	epMu.Unlock()
	return changed
}

// measureAll measures every endpoint in cfg concurrently and records the results.
func measureAll(ctx context.Context, cfg map[string][]Endpoint) {
	type result struct {
		provider, endpoint string
		m                  measurement
	}
	results := make(chan result)
	n := 0
	for id, eps := range cfg {
		for _, e := range eps {
			n++
			go func(id string, e Endpoint) {
				latency, err := measureFn(ctx, e.URL)
				results <- result{id, e.ID, measurement{ok: err == nil, latency: latency}}
			}(id, e)
		}
	}
	fresh := map[string]map[string]measurement{}
	for range n {
		r := <-results
		if fresh[r.provider] == nil {
			fresh[r.provider] = map[string]measurement{}
		}
		fresh[r.provider][r.endpoint] = r.m
	}
	epMu.Lock()
	for id, m := range fresh {
		epMeasured[id] = m
	}
	epMu.Unlock()
}

// endpointClient makes measurement requests: a new connection each time, as
// every opencode process opens one, and redirects are answers, not to follow.
var endpointClient = func() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DisableKeepAlives = true
	return &http.Client{
		Transport:     t,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}()

// measureEndpoint times one request to an endpoint's root. Any HTTP answer
// counts: the request carries no credentials, so a refusal still proves the
// path is open, and it is the time to answer that's being compared.
func measureEndpoint(ctx context.Context, endpointURL string) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, endpointMeasureTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpointURL+"/", nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := endpointClient.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return time.Since(start), nil
}

func sortedEndpointProviders(cfg map[string][]Endpoint) []string {
	ids := make([]string, 0, len(cfg))
	for id := range cfg {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func logChoice(providerID string, eps []Endpoint) {
	epMu.RLock()
	active, auto, m := epActive[providerID], epAuto[providerID], epMeasured[providerID]
	epMu.RUnlock()
	how := "chosen by the user"
	if auto {
		how = "fastest reachable"
	}
	parts := make([]string, 0, len(eps))
	for _, e := range eps {
		r, ok := m[e.ID]
		switch {
		case !ok:
			parts = append(parts, e.ID+" not measured")
		case !r.ok:
			parts = append(parts, e.ID+" unreachable")
		default:
			parts = append(parts, fmt.Sprintf("%s %d ms", e.ID, r.latency.Milliseconds()))
		}
	}
	log.Printf("[providers] %s endpoint: %s (%s; %s)", providerID, active, how, strings.Join(parts, ", "))
}

// withEndpoints adds a provider's endpoints and the one in use to its record.
func withEndpoints(p Provider, cfg map[string][]Endpoint) Provider {
	eps := cfg[p.ID]
	if len(eps) == 0 {
		return p
	}
	epMu.RLock()
	defer epMu.RUnlock()
	m := epMeasured[p.ID]
	p.Endpoints = make([]EndpointStatus, 0, len(eps))
	for _, e := range eps {
		s := EndpointStatus{ID: e.ID, Label: e.Label}
		if r, ok := m[e.ID]; ok {
			s.Measured, s.Reachable = true, r.ok
			if r.ok {
				s.LatencyMs = max(r.latency.Milliseconds(), 1)
			}
		}
		p.Endpoints = append(p.Endpoints, s)
	}
	p.Endpoint, p.EndpointAuto = epActive[p.ID], epAuto[p.ID]
	return p
}

// envPlaceholder matches opencode's {env:NAME} substitution.
var envPlaceholder = regexp.MustCompile(`\{env:([^}]+)\}`)

func expandEnv(s string) string {
	return envPlaceholder.ReplaceAllStringFunc(s, func(m string) string {
		return os.Getenv(envPlaceholder.FindStringSubmatch(m)[1])
	})
}

// RequestConfig returns the base URL and headers the platform configuration
// gives opencode for a provider, with {env:NAME} placeholders resolved, so a
// reachability check can call the address opencode will. Both are empty when
// the configuration sets none.
func RequestConfig(providerID string) (baseURL string, headers map[string]string) {
	opts, _ := configuredBlock(providerID)["options"].(map[string]any)
	if s, ok := opts["baseURL"].(string); ok {
		baseURL = expandEnv(s)
	}
	if h, ok := opts["headers"].(map[string]any); ok {
		for k, v := range h {
			if s, ok := v.(string); ok {
				if headers == nil {
					headers = map[string]string{}
				}
				headers[k] = expandEnv(s)
			}
		}
	}
	return baseURL, headers
}

// HandleSetEndpoint serves POST /api/providers/{id}/endpoint with
// {"endpoint": "<id>" | "auto"}.
func HandleSetEndpoint(w http.ResponseWriter, r *http.Request) {
	p, ok := lookup(strings.TrimSpace(r.PathValue("id")))
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown provider")
		return
	}
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := SelectEndpoint(r.Context(), p.ID, body.Endpoint); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
