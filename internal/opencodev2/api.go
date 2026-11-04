// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodev2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Get decodes the JSON the server returns for path into out.
func Get(ctx context.Context, path string, out any) error {
	base, password, err := Ensure(ctx)
	if err != nil {
		return err
	}
	return get(ctx, base, password, path, out)
}

// Post sends body as JSON to path and decodes the reply into out, when out
// isn't nil.
func Post(ctx context.Context, path string, body, out any) error {
	base, password, err := Ensure(ctx)
	if err != nil {
		return err
	}
	return do(ctx, http.MethodPost, base, password, path, body, out)
}

// Delete sends a DELETE for path.
func Delete(ctx context.Context, path string) error {
	base, password, err := Ensure(ctx)
	if err != nil {
		return err
	}
	return do(ctx, http.MethodDelete, base, password, path, nil, nil)
}

// HasVariant reports whether the model ("provider/model") offers the variant
// (a reasoning level). It is true when the server can't say, or doesn't list
// the model: only a known model without the variant is false.
func HasVariant(ctx context.Context, model, variant string) bool {
	provider, id, ok := strings.Cut(model, "/")
	if !ok {
		return true
	}
	var resp struct {
		Data []struct {
			ID         string `json:"id"`
			ProviderID string `json:"providerID"`
			Variants   []struct {
				ID string `json:"id"`
			} `json:"variants"`
		} `json:"data"`
	}
	if err := Get(ctx, "/api/model", &resp); err != nil {
		return true
	}
	for _, m := range resp.Data {
		if m.ProviderID != provider || m.ID != id {
			continue
		}
		for _, v := range m.Variants {
			if v.ID == variant {
				return true
			}
		}
		return false
	}
	return true
}

// Interrupt stops the turn running in an opencode session. Ending the
// `opencode run` that started it doesn't: the server carries on.
func Interrupt(ctx context.Context, sessionID string) error {
	base, password, ok := Running()
	if !ok || sessionID == "" {
		return nil
	}
	return do(ctx, http.MethodPost, base, password, "/api/session/"+url.PathEscape(sessionID)+"/interrupt", nil, nil)
}

// apiError is a request the server refused.
type apiError struct {
	method, path, status, body string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s %s: %s %s", e.method, e.path, e.status, e.body)
}

// withoutReply drops the server's reply from a refused request's error. A
// reply to a request that carried a secret can quote it.
func withoutReply(err error) error {
	var e *apiError
	if errors.As(err, &e) {
		return fmt.Errorf("%s %s: %s", e.method, e.path, e.status)
	}
	return err
}

func get(ctx context.Context, base, password, path string, out any) error {
	return do(ctx, http.MethodGet, base, password, path, nil, out)
}

func do(ctx context.Context, method, base, password, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, r)
	if err != nil {
		return err
	}
	req.SetBasicAuth("opencode", password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &apiError{method: method, path: path, status: resp.Status, body: string(bytes.TrimSpace(msg))}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
