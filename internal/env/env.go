// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package env reads Knowledge Worker Agent's settings from the environment.
// Each setting has a KWA_ name and is also read under the names it had before,
// so existing setups keep working until a later release stops reading them.
package env

import (
	"os"
	"sort"
	"strings"
)

// oldNames lists, for each setting, the names it had before, the first one
// read first.
var oldNames = map[string][]string{
	"KWA_HOST":          {"COPILOT_WEB_HOST"},
	"KWA_PORT":          {"COPILOT_WEB_PORT"},
	"KWA_DB_BACKUP":     {"COPILOT_DB_BACKUP"},
	"KWA_LOG_FILE":      {"COPILOT_LOG_FILE"},
	"KWA_LOG_MAX_BYTES": {"COPILOT_LOG_MAX_BYTES"},

	"KWA_OPENCODE_BIN":             {"OPENCODE_BIN"},
	"KWA_OPENCODE_AUTO_UPDATE":     {"OPENCODE_AUTO_UPDATE", "COPILOT_AUTO_UPDATE"},
	"KWA_OPENCODE_INSTALL_DIR":     {"OPENCODE_INSTALL_DIR"},
	"KWA_OPENCODE_USE_SERVER":      {"OPENCODE_USE_SERVER"},
	"KWA_OPENCODE_STARTUP_TIMEOUT": {"OPENCODE_STARTUP_TIMEOUT"},
	"KWA_OPENCODE_MCP_CONFIG_PATH": {"OPENCODE_MCP_CONFIG_PATH"},

	"KWA_PROJECT_ID":      {"SDS_PROJECT_ID"},
	"KWA_CONFIG_REPO_URL": {"SDS_CONFIG_REPO_URL"},
	"KWA_CENTRAL_CONFIG":  {"SDS_CENTRAL_CONFIG"},
	"KWA_ORGANIZATION_ID": {"SDS_ORGANIZATION_ID"},
	"KWA_REGION":          {"SDS_REGION"},

	"KWA_GCLOUD_BIN":       {"GCLOUD_BIN"},
	"KWA_GITHUB_CLIENT_ID": {"OPENCODE_GITHUB_CLIENT_ID", "COPILOT_GITHUB_CLIENT_ID"},

	"KWA_OBSIDIAN_AUTO_INSTALL": {"COPILOT_OBSIDIAN_AUTO_INSTALL"},
	"KWA_OBSIDIAN_VAULT":        {"COPILOT_OBSIDIAN_VAULT"},

	"KWA_AUTO_TITLE":                 {"CHAT_AUTO_TITLE"},
	"KWA_SCHEDULED_TASKS":            {"SCHEDULED_TASKS"},
	"KWA_SCHEDULED_TASKS_INTERVAL":   {"SCHEDULED_TASKS_INTERVAL"},
	"KWA_PROJECT_AUTO_SYNC":          {"PROJECT_AUTO_SYNC"},
	"KWA_PROJECT_AUTO_SYNC_INTERVAL": {"PROJECT_AUTO_SYNC_INTERVAL"},
	"KWA_WORKSPACE_HEARTBEAT":        {"SDS_WORKSPACE_HEARTBEAT"},
	"KWA_HYGIENE_ENABLED":            {"SDS_HYGIENE_ENABLED"},
	"KWA_HYGIENE_REVIEW_AFTER":       {"SDS_HYGIENE_REVIEW_AFTER"},
	"KWA_HYGIENE_SNOOZE":             {"SDS_HYGIENE_SNOOZE"},
	"KWA_HYGIENE_EMPTY_AFTER":        {"SDS_HYGIENE_EMPTY_AFTER"},
	"KWA_VOICE":                      {"SDS_VOICE"},
	"KWA_VOICE_BIN":                  {"SDS_VOICE_BIN"},
	"KWA_VOICE_MODEL":                {"SDS_VOICE_MODEL"},
	"KWA_GUEST_PORT":                 {"SDS_CHAT_GUEST_PORT"},
	"KWA_GUEST_ORIGIN":               {"SDS_CHAT_GUEST_ORIGIN"},
	"KWA_SIDECAR_SOCKET":             {"SDS_SIDECAR_SOCKET"},
}

// ReplacedByHome lists the old location settings that KWA_HOME replaces.
// Where they're set, they're still read as before.
var ReplacedByHome = []string{
	"COPILOT_WORKSPACE",
	"COPILOT_DB_PATH",
	"SDS_CONFIG_CACHE_DIR",
	"COPILOT_OBSIDIAN_INSTALL_DIR",
	"COPILOT_HOME",
	"COPILOT_GITHUB_PAT_PATH",
}

// Get returns the setting's value, or else the value of the first of its old
// names that's set. A blank value counts as unset.
func Get(name string) string {
	for _, n := range Names(name) {
		if v := os.Getenv(n); strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// Names returns the setting's name and its old names, in the order Get reads
// them.
func Names(name string) []string {
	return append([]string{name}, oldNames[name]...)
}

// OldInUse lists each old name that's set, sorted, with the name to use
// instead.
func OldInUse() []string {
	var out []string
	for name, olds := range oldNames {
		for _, old := range olds {
			if strings.TrimSpace(os.Getenv(old)) != "" {
				out = append(out, old+" (now "+name+")")
			}
		}
	}
	for _, old := range ReplacedByHome {
		if strings.TrimSpace(os.Getenv(old)) != "" {
			out = append(out, old+" (KWA_HOME replaces it)")
		}
	}
	sort.Strings(out)
	return out
}
