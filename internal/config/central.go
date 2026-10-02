// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

// EnvCentralConfig is the single switch that puts a workspace under central
// management.
//
// It governs every centrally managed surface together:
//
//   - model providers — the providers the config repo assigned are offered
//     instead of the built-in Copilot/Vertex pair, falling back to the
//     built-ins if nothing usable resolves, so a misconfigured repo cannot
//     leave a workspace unable to chat;
//   - MCP servers — the built-in catalogue is not installed and untouched
//     entries it wrote on earlier boots are conservatively removed, leaving the
//     workspace's catalogue to the administrator.
//
// It does not govern agents, skills or context: the binary no longer ships any,
// so the config repo is their only source in every workspace. What
// internal/defaults still installs — the document skills and the opencode
// plugins — is the capability floor and is installed unconditionally, as is the
// retirement of the content bundle older versions used to install.
//
// These were once three separate variables (SDS_CENTRAL_CONFIG,
// SDS_CENTRAL_MODELS, SDS_CENTRAL_MCP) so the migrations could be rolled out
// one at a time. That is over: partially-central workspaces were the source of
// the confusing states — centrally assigned agents calling models the workspace
// did not offer, or MCP servers the config repo never mentioned — and an
// administrator turning on central management means all of it. The obsolete
// variables are no longer read.
//
// Off unless explicitly enabled, so an existing workspace keeps its defaults.
const EnvCentralConfig = "KWA_CENTRAL_CONFIG"

// CentralConfigEnabled reports whether the workspace is centrally managed.
//
// Values follow the convention used by the other boolean switches in this repo:
// "1"/"true"/"yes"/"on", case-insensitive. Anything else — including unset,
// empty or an unrecognised value — keeps the un-managed behaviour.
func CentralConfigEnabled() bool {
	return envBool(EnvCentralConfig, false)
}
