// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"reflect"
	"testing"
)

func TestShadowedServersReportsOnlyTheUsersOwnDefinitions(t *testing.T) {
	withPlatformConfig(t, `{"mcp":{
		"atlassian":{"type":"remote","url":"https://a.example.com"},
		"github":{"type":"remote","url":"https://g.example.com"}}}`, "")
	withGlobalConfig(t, `{"mcp":{
		"Atlassian":{"type":"remote","url":"https://mine.example.com"},
		"github":{"enabled":true},
		"local-only":{"type":"local","command":["x"]}}}`)
	// Atlassian: the user's own definition, hidden. github: only the toggle the
	// provisioning stub carries. local-only: nothing assigned of that name.
	if got, want := ShadowedServers(), []string{"Atlassian"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ShadowedServers() = %v, want %v", got, want)
	}
}

func TestShadowedServersWithNothingAssigned(t *testing.T) {
	withPlatformConfig(t, "", "")
	withGlobalConfig(t, `{"mcp":{"atlassian":{"type":"remote","url":"https://mine.example.com"}}}`)
	if got := ShadowedServers(); len(got) != 0 {
		t.Fatalf("ShadowedServers() = %v, want none", got)
	}
}
