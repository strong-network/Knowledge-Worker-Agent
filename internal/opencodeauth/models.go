// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// modelsTimeout bounds the `opencode models` call. Model listing reads a local
// cache (refreshable via --refresh) so it is normally fast, but the first call
// after install may hit models.dev.
var modelsTimeout = 60 * time.Second

// FetchModelsFor returns the list of opencode models for an arbitrary provider
// id (e.g. "github-copilot" or "google-vertex"), as "provider/model"
// identifiers. It shells out to `opencode models <provider>`, whose output is
// one identifier per line. This lets callers discover models for whichever
// provider(s) the user has actually authenticated (GitHub Copilot and/or
// Google Cloud Vertex AI).
//
// Returns ([]string{}, err) on failure so callers can keep any previously
// cached list. The slice is always non-nil.
func FetchModelsFor(provider string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelsTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, resolveBin(), "models", provider).CombinedOutput()
	if err != nil {
		return []string{}, describeFailure(ctx, err, string(out))
	}
	return parseModels(string(out)), nil
}

// describeFailure turns an `opencode models` failure into something a boot log
// can be diagnosed from.
//
// A timeout kills the child, so exec reports only "signal: killed" — which does
// not say whether the call was stuck contacting models.dev, refreshing auth, or
// something else, nor even that it was a timeout rather than a crash. Naming the
// deadline and attaching whatever the subprocess printed before it died turns
// that dead end into a starting point.
func describeFailure(ctx context.Context, err error, out string) error {
	detail := lastMeaningfulLine(out)
	timedOut := ctx.Err() == context.DeadlineExceeded
	switch {
	case timedOut && detail != "":
		return fmt.Errorf("timed out after %s (last output: %s)", modelsTimeout, detail)
	case timedOut:
		return fmt.Errorf("timed out after %s with no output", modelsTimeout)
	case detail != "":
		return fmt.Errorf("%w: %s", err, detail)
	default:
		return err
	}
}

// maxDetailLen bounds the captured subprocess output copied into an error, so a
// runaway child cannot dump an unbounded blob into the boot log.
const maxDetailLen = 200

// lastMeaningfulLine returns the final non-empty line of the subprocess output,
// stripped of ANSI colour and truncated. The last line is the useful one: it is
// whatever the process was reporting when it was killed.
func lastMeaningfulLine(raw string) string {
	clean := ansiRe.ReplaceAllString(raw, "")
	lines := strings.Split(clean, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if len(line) > maxDetailLen {
			return line[:maxDetailLen] + "…"
		}
		return line
	}
	return ""
}

// parseModels turns the line-oriented output of `opencode models <provider>`
// into a sorted, de-duplicated slice of "provider/model" identifiers. It is
// tolerant of blank lines, ANSI colour codes, and stray non-model log lines:
// only lines that look like "<provider>/<model>" are kept.
func parseModels(raw string) []string {
	clean := ansiRe.ReplaceAllString(raw, "")
	seen := make(map[string]bool)
	var models []string
	for _, line := range strings.Split(clean, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// A valid identifier is exactly "provider/model" with no whitespace.
		if strings.ContainsAny(line, " \t") {
			continue
		}
		slash := strings.IndexByte(line, '/')
		if slash <= 0 || slash == len(line)-1 {
			continue
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		models = append(models, line)
	}
	sort.Strings(models)
	if models == nil {
		return []string{}
	}
	return models
}
