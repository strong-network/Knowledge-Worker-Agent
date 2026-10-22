// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package vertexauth

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Neither Vertex nor an AI gateway in front of it lists the models a project
// may use, so CheckModels asks about each one. It sends a request with nothing
// to answer, which the service rejects before any text is generated:
//
//   - 400, the model's own complaint about the request: the request reached
//     the model, so it is served.
//   - 403 or 404, or a 400 citing an organization policy: it is not.
//   - Anything else (401, 429, 5xx, no answer) says nothing about the model.

// API is the interface a model is reached through, from its catalogue entry.
type API int

const (
	APIUnknown API = iota
	// APIAnthropic is Claude, through Vertex's Anthropic publisher.
	APIAnthropic
	// APIGemini is Google's own models.
	APIGemini
	// APIOpenAI is a partner model on Vertex's OpenAI-compatible endpoint.
	APIOpenAI
)

// APIFor maps an opencode client package to the interface it speaks.
func APIFor(npm string) API {
	switch npm {
	case "@ai-sdk/google-vertex/anthropic":
		return APIAnthropic
	case "@ai-sdk/google-vertex":
		return APIGemini
	case "@ai-sdk/openai-compatible":
		return APIOpenAI
	}
	return APIUnknown
}

// Candidate is one model to check.
type Candidate struct {
	Model string // the id after the provider, e.g. claude-sonnet-5@default
	API   API
	// URL is the catalogue's address template for an APIOpenAI model.
	URL string
}

// Availability is what a check found out about one model.
type Availability int

const (
	Unknown Availability = iota
	Available
	Unavailable
)

// modelCheckTimeout bounds one model's request; modelCheckWorkers how many
// run at once.
var (
	modelCheckTimeout = 10 * time.Second
	modelCheckWorkers = 8
)

// CheckModels reports which candidates this workspace can use, through
// target's BaseURL and Headers when set, as opencode would reach them. It
// fails only when no request can be made at all.
func CheckModels(ctx context.Context, target Target, cands []Candidate) (map[string]Availability, error) {
	token, err := accessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not obtain Google credentials: %v", err)
	}
	project, location := Project(), Location()
	if project == "" && target.BaseURL == "" {
		return nil, fmt.Errorf("no Vertex project is configured")
	}

	out := make(map[string]Availability, len(cands))
	checkAll(ctx, token, target, project, location, cands, out)
	// A busy gateway drops the odd request; one more try keeps a served model
	// from vanishing until the next refresh.
	var retry []Candidate
	for _, c := range cands {
		if out[c.Model] == Unknown {
			retry = append(retry, c)
		}
	}
	checkAll(ctx, token, target, project, location, retry, out)
	return out, nil
}

func checkAll(ctx context.Context, token string, target Target, project, location string, cands []Candidate, out map[string]Availability) {
	var mu sync.Mutex
	work := make(chan Candidate)
	var wg sync.WaitGroup
	for range modelCheckWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range work {
				a := checkModel(ctx, token, target, project, location, c)
				mu.Lock()
				out[c.Model] = a
				mu.Unlock()
			}
		}()
	}
	for _, c := range cands {
		work <- c
	}
	close(work)
	wg.Wait()
}

func checkModel(ctx context.Context, token string, target Target, project, location string, c Candidate) Availability {
	url, body := modelRequest(target.BaseURL, project, location, c)
	if url == "" {
		return Unknown
	}
	ctx, cancel := context.WithTimeout(ctx, modelCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return Unknown
	}
	for k, v := range target.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[vertexauth] no answer about %s: %v", c.Model, err)
		return Unknown
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	a := classify(resp.StatusCode, raw)
	if a == Unknown {
		log.Printf("[vertexauth] no answer about %s: HTTP %d", c.Model, resp.StatusCode)
	}
	return a
}

// modelRequest returns the address and body that ask about one model. A base
// URL, when configured, replaces every address, as opencode passes it to each
// client package.
func modelRequest(base, project, location string, c Candidate) (url, body string) {
	base = strings.TrimRight(base, "/")
	root := fmt.Sprintf("https://%s/v1/projects/%s/locations/%s", apiHost(location), project, location)
	switch c.API {
	case APIAnthropic:
		if base == "" {
			base = root + "/publishers/anthropic/models"
		}
		return base + "/" + c.Model + ":rawPredict", "{}"
	case APIGemini:
		if base == "" {
			base = root + "/publishers/google/models"
		}
		return base + "/" + c.Model + ":generateContent", "{}"
	case APIOpenAI:
		if base == "" {
			base = expandVertexURL(c.URL, project, location)
		}
		if base == "" {
			return "", ""
		}
		// With no messages at all the endpoint rejects the request before it
		// looks the model up; an empty list makes it look first.
		return base + "/chat/completions",
			fmt.Sprintf(`{"model":%q,"messages":[],"max_tokens":1}`, c.Model)
	}
	return "", ""
}

// expandVertexURL fills in the placeholders of a catalogue address template,
// as opencode does.
func expandVertexURL(tmpl, project, location string) string {
	return strings.NewReplacer(
		"${GOOGLE_VERTEX_PROJECT}", project,
		"${GOOGLE_VERTEX_LOCATION}", location,
		"${GOOGLE_VERTEX_ENDPOINT}", apiHost(location),
	).Replace(tmpl)
}

func classify(status int, body []byte) Availability {
	switch status {
	case http.StatusBadRequest:
		if bytes.Contains(body, []byte("Organization Policy")) || bytes.Contains(body, []byte("allowedModels")) {
			return Unavailable
		}
		return Available
	case http.StatusForbidden, http.StatusNotFound:
		return Unavailable
	}
	return Unknown
}
