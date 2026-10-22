// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/opencodeauth"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/providers"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/vertexauth"
)

// vertexDiscoveryTimeout bounds checking every model in the catalogue.
const vertexDiscoveryTimeout = 60 * time.Second

// lastVertexModels holds the last discovery, for the address it was made
// through: a model the next check cannot answer for keeps its last answer, and
// a refresh that cannot check at all keeps the last list.
var lastVertexModels struct {
	sync.Mutex
	base      string
	available map[string]bool
}

// discoverVertexModels lists the Vertex models this workspace can use. Vertex
// offers more models than a project is allowed, and an AI gateway in front of
// it fewer still, and neither lists them, so each chat model in opencode's
// catalogue is checked (see vertexauth.CheckModels).
func discoverVertexModels() ([]string, error) {
	start := time.Now()
	details, err := opencodeauth.FetchModelDetails(vertexauth.Provider)
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	var cands []vertexauth.Candidate
	for _, d := range details {
		api := vertexauth.APIFor(d.NPM)
		if chatModel(d) && api != vertexauth.APIUnknown {
			ids[d.Model] = d.ID
			cands = append(cands, vertexauth.Candidate{Model: d.Model, API: api, URL: d.URL})
		}
	}

	base, headers := providers.RequestConfig(vertexauth.Provider)
	ctx, cancel := context.WithTimeout(context.Background(), vertexDiscoveryTimeout)
	defer cancel()
	checkStart := time.Now()
	states, checkErr := vertexauth.CheckModels(ctx, vertexauth.Target{BaseURL: base, Headers: headers}, cands)

	lastVertexModels.Lock()
	defer lastVertexModels.Unlock()
	if lastVertexModels.base != base {
		lastVertexModels.base, lastVertexModels.available = base, nil
	}
	if checkErr != nil {
		if lastVertexModels.available == nil {
			return nil, checkErr
		}
		fmt.Fprintf(os.Stderr, "  ⚠ Vertex models: could not check (%v); keeping the last list\n", checkErr)
		return sortedKeys(lastVertexModels.available), nil
	}
	available := vertexAvailable(ids, states, lastVertexModels.available)
	lastVertexModels.available = available

	counts := map[vertexauth.Availability]int{}
	for _, s := range states {
		counts[s]++
	}
	fmt.Fprintf(os.Stderr, "  · Vertex models: %d of %d available (%d not, %d unanswered); checked in %s of %s\n",
		len(available), len(cands), counts[vertexauth.Unavailable], counts[vertexauth.Unknown],
		time.Since(checkStart).Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
	return sortedKeys(available), nil
}

// chatModel reports whether a catalogue entry is a model the chat can use:
// one that calls tools and answers in text only. That leaves out image,
// speech and embedding models, which a check would also find available.
func chatModel(d opencodeauth.ModelDetail) bool {
	return d.ToolCall && d.OutputText && !d.OutputImage && !d.OutputAudio && d.Status != "deprecated"
}

// vertexAvailable returns the models confirmed available, plus the ones whose
// check went unanswered this time and were available last time. ids maps each
// candidate's model id to its "provider/model" id.
func vertexAvailable(ids map[string]string, states map[string]vertexauth.Availability, last map[string]bool) map[string]bool {
	out := map[string]bool{}
	for model, id := range ids {
		switch states[model] {
		case vertexauth.Available:
			out[id] = true
		case vertexauth.Unknown:
			if last[id] {
				out[id] = true
			}
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
