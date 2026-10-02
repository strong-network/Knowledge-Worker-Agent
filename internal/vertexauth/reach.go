// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package vertexauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// CheckReachable tests whether this workspace can actually call Vertex, as
// opposed to merely holding credentials for it.
//
// The distinction is not academic: a Google Cloud organization can place
// aiplatform.googleapis.com behind a VPC Service Controls perimeter, which
// refuses requests from outside it with a 403 even when the caller's
// credentials are valid. Every credential-shaped check — an ADC file on disk, a
// mintable access token, a model catalogue listing — passes in that state,
// which is why this one issues a real (one-token) request instead.
//
// A refusal is returned with Google's own message, because "prohibited by
// organization's policy" is the only part of this a user can act on.
func CheckReachable(ctx context.Context) error {
	token, err := accessToken(ctx)
	if err != nil {
		return fmt.Errorf("could not obtain Google credentials: %v", err)
	}

	project, location := Project(), Location()
	if project == "" {
		return fmt.Errorf("no Vertex project is configured")
	}
	url := fmt.Sprintf(
		"https://%s/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:streamRawPredict",
		apiHost(location), project, location, reachProbeModel)

	body, _ := json.Marshal(map[string]any{
		"anthropic_version": "vertex-2023-10-16",
		"max_tokens":        1,
		"messages":          []map[string]string{{"role": "user", "content": "hi"}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("Vertex did not respond in time")
		}
		return fmt.Errorf("could not reach Vertex: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// A 400 means the request reached Vertex and was understood well enough to
	// be rejected on its contents — the path works, which is what is being
	// tested here.
	if resp.StatusCode == http.StatusBadRequest {
		return nil
	}
	return fmt.Errorf("%s", vertexErrorMessage(resp.StatusCode, raw))
}

// reachProbeModel is the model the probe asks for. Any Claude model on Vertex
// would do; the request is capped at one token.
const reachProbeModel = "claude-sonnet-5@default"

// apiHost returns the Vertex endpoint for a location. The "global" location
// uses the unprefixed host; every region has its own.
func apiHost(location string) string {
	if location == "" || location == "global" {
		return "aiplatform.googleapis.com"
	}
	return location + "-aiplatform.googleapis.com"
}

// vertexErrorMessage turns a Vertex error body into one line a user can act on.
func vertexErrorMessage(status int, raw []byte) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	// Vertex answers some errors with a JSON array containing the object.
	trimmed := bytes.TrimSpace(raw)
	if bytes.HasPrefix(trimmed, []byte("[")) {
		var arr []json.RawMessage
		if json.Unmarshal(trimmed, &arr) == nil && len(arr) > 0 {
			trimmed = arr[0]
		}
	}
	_ = json.Unmarshal(trimmed, &payload)

	msg := strings.TrimSpace(payload.Error.Message)
	if msg == "" {
		return fmt.Sprintf("Vertex refused the request (HTTP %d)", status)
	}
	// The VPC-SC identifier is a long opaque support reference; keeping it in a
	// UI string buries the sentence that matters.
	if i := strings.Index(msg, "vpcServiceControlsUniqueIdentifier"); i >= 0 {
		msg = strings.TrimSpace(strings.TrimRight(msg[:i], " .")) +
			". Your organization's VPC Service Controls perimeter is blocking this workspace."
	}
	return msg
}

// accessToken returns a bearer token for the ADC credentials.
func accessToken(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, resolveBin(),
		"auth", "application-default", "print-access-token", "--quiet").Output()
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", fmt.Errorf("gcloud returned no token")
	}
	return tok, nil
}

// SignOut discards this workspace's Google credentials so a different provider
// can be used instead.
//
// `gcloud auth application-default revoke` is attempted first so the refresh
// token is invalidated upstream rather than merely forgotten locally, but the
// file is removed either way: a revoke that fails offline must not leave the
// workspace still believing it is signed in.
func SignOut() error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	revokeErr := exec.CommandContext(ctx, resolveBin(),
		"auth", "application-default", "revoke", "--quiet").Run()

	path := adcCredentialsPath()
	if path == "" {
		if revokeErr != nil {
			return fmt.Errorf("could not revoke Google credentials: %v", revokeErr)
		}
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not remove the credentials file: %v", err)
	}
	return nil
}
