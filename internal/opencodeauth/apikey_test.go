// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodeauth

import (
	"context"
	"errors"
	"testing"
)

// credStore stands in for opencode's credential API.
type credStore struct {
	values map[string]map[string]any
	labels map[string]string
	down   bool // the server can't answer
}

func stubCredentials(t *testing.T) *credStore {
	t.Helper()
	s := &credStore{values: map[string]map[string]any{}, labels: map[string]string{}}
	oldSet, oldRemove, oldHas := setCredential, removeCredentials, hasCredential
	setCredential = func(_ context.Context, integration, label string, value map[string]any) error {
		if s.down {
			return errors.New("no server")
		}
		s.values[integration], s.labels[integration] = value, label
		return nil
	}
	removeCredentials = func(_ context.Context, integration string) error {
		if s.down {
			return errors.New("no server")
		}
		delete(s.values, integration)
		return nil
	}
	hasCredential = func(_ context.Context, integration string) (bool, error) {
		if s.down {
			return false, errors.New("no server")
		}
		_, ok := s.values[integration]
		return ok, nil
	}
	t.Cleanup(func() { setCredential, removeCredentials, hasCredential = oldSet, oldRemove, oldHas })
	return s
}

func TestSetAPIKeyStoresAKey(t *testing.T) {
	s := stubCredentials(t)
	if err := SetAPIKey(" mistral ", "sk-abc"); err != nil {
		t.Fatal(err)
	}
	if v := s.values["mistral"]; v["type"] != "key" || v["key"] != "sk-abc" {
		t.Errorf("stored %v, want a key credential", v)
	}
	if !HasCredential("mistral") || HasCredential("anthropic") {
		t.Error("HasCredential doesn't match what was stored")
	}
}

func TestSetAPIKeyRejectsEmptyInput(t *testing.T) {
	s := stubCredentials(t)
	if SetAPIKey("", "sk-abc") == nil || SetAPIKey("mistral", "  ") == nil {
		t.Fatal("accepted an empty provider id or key")
	}
	if len(s.values) != 0 {
		t.Errorf("stored %v", s.values)
	}
}

func TestRemoveAPIKey(t *testing.T) {
	stubCredentials(t)
	if err := SetAPIKey("mistral", "sk-abc"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAPIKey("mistral"); err != nil {
		t.Fatal(err)
	}
	if HasCredential("mistral") {
		t.Error("the key is still there")
	}
	// Removing a key that isn't there is what the caller wanted anyway.
	if err := RemoveAPIKey("mistral"); err != nil {
		t.Errorf("removing a missing key: %v", err)
	}
}

// When opencode's server can't answer, nothing reads as signed in.
func TestHasCredentialWithoutTheServer(t *testing.T) {
	s := stubCredentials(t)
	if err := SetAPIKey("mistral", "sk-abc"); err != nil {
		t.Fatal(err)
	}
	s.down = true
	if HasCredential("mistral") {
		t.Error("reported a credential the server couldn't confirm")
	}
}
