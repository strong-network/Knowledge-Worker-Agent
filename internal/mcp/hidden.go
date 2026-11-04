// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"slices"
	"sort"
	"strings"
)

// hiddenValue stands in for a header or environment value in what the API
// returns. They are usually credentials (a bearer token, an API key), and a
// response can be read by more than the person using the app.
const hiddenValue = "••••••••"

// withHiddenValues returns sv as the API shows it.
func (sv serverView) withHiddenValues() serverView {
	sv.Headers = hideValues(sv.Headers)
	sv.Environment = hideValues(sv.Environment)
	return sv
}

func hideValues(m map[string]string) map[string]string {
	if len(m) == 0 {
		return m
	}
	out := make(map[string]string, len(m))
	for k := range m {
		out[k] = hiddenValue
	}
	return out
}

// restoreHiddenValues puts the stored values back where a request still holds
// hiddenValue, as an edit that leaves them alone does. Only for the server
// they were saved with, at the same address or command, so a stored credential
// never goes somewhere new; otherwise it names the values to enter again.
func restoreHiddenValues(sv *serverView, stored serverView, have bool) error {
	var names []string
	for _, m := range []map[string]string{sv.Headers, sv.Environment} {
		for k, v := range m {
			if v == hiddenValue {
				names = append(names, k)
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	if !have || !sameTarget(*sv, stored) {
		sort.Strings(names)
		return &hiddenError{names: names}
	}
	restore(sv.Headers, stored.Headers)
	restore(sv.Environment, stored.Environment)
	return nil
}

func restore(m, stored map[string]string) {
	for k, v := range m {
		if v != hiddenValue {
			continue
		}
		if old, ok := stored[k]; ok {
			m[k] = old
		} else {
			delete(m, k)
		}
	}
}

// sameTarget reports whether a and b reach the same server: the same URL, or
// the same command and arguments.
func sameTarget(a, b serverView) bool {
	if isRemote(a) != isRemote(b) {
		return false
	}
	if isRemote(a) {
		return strings.TrimSpace(a.URL) == strings.TrimSpace(b.URL)
	}
	return strings.TrimSpace(a.Command) == strings.TrimSpace(b.Command) && slices.Equal(a.Args, b.Args)
}

func isRemote(sv serverView) bool {
	switch strings.ToLower(sv.Transport) {
	case "http", "sse", "remote":
		return true
	}
	return sv.Type == "remote"
}

type hiddenError struct{ names []string }

func (e *hiddenError) Error() string {
	return "Enter " + strings.Join(e.names, ", ") + " again: a saved value is only used with the address or command it was saved for."
}

// restoreHiddenValues lets a check of the server being edited use its saved
// values, under the same rule as an edit.
func (b *probeRequest) restoreHiddenValues(transport string) error {
	if !hasHidden(b.Headers) && !hasHidden(b.Env) {
		return nil
	}
	sv := serverView{Transport: transport, URL: b.URL, Command: b.Command, Args: b.Args, Headers: b.Headers, Environment: b.Env}
	stored, have, err := getServer(strings.TrimSpace(b.Name))
	if err != nil || strings.TrimSpace(b.Name) == "" {
		have = false
	}
	if err := restoreHiddenValues(&sv, stored, have); err != nil {
		return err
	}
	b.Headers, b.Env = sv.Headers, sv.Environment
	return nil
}

func hasHidden(m map[string]string) bool {
	for _, v := range m {
		if v == hiddenValue {
			return true
		}
	}
	return false
}
