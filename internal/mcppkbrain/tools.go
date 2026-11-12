// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcppkbrain

func toolDefs() []any {
	return []any{
		map[string]any{
			"name": "pkbrain_search",
			"description": "On-demand search of imported ChatGPT and other-agent discussions archived in PKBrain. " +
				"Use this for history imported from outside KWA; use KWA Recall for chats created in KWA. " +
				"Do not search automatically on every turn. Start with two or three distinctive terms. " +
				"Returns up to 5 short message excerpts by default, including filename, source ID, project/provenance, role, and branch when known. " +
				"The response reports returned_excerpt_count, returned_bytes (the PKBrain JSON response size), and truncated. " +
				"If an excerpt is not enough, call pkbrain_fetch for that source ID. This byte count is separate from KWA's total input-token meter.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":     map[string]any{"type": "string", "description": "Distinctive words or a short phrase to find in imported discussions."},
					"project":   map[string]any{"type": "string", "description": "Optional exact project ID or name."},
					"limit":     map[string]any{"type": "integer", "description": "Number of excerpts. Defaults to 5; maximum 10."},
					"max_bytes": map[string]any{"type": "integer", "description": "Maximum PKBrain JSON response size in bytes. Defaults to 8192; maximum 16384."},
				},
				"required": []string{"query"},
			},
		},
		map[string]any{
			"name": "pkbrain_fetch",
			"description": "Explicit follow-up to pkbrain_search: fetch a bounded page from one canonical PKBrain Markdown discussion by its discussion_id or source ID. " +
				"Use only when the returned excerpts need more context. The default response is capped at 16 KiB and the maximum is 64 KiB. " +
				"Check truncated and next_offset_bytes, then pass that offset to fetch another page if needed. " +
				"This reads imported ChatGPT/other-agent history; KWA Recall reads KWA's own chats.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"source_id": map[string]any{"type": "string", "description": "Prefer discussion_id returned by pkbrain_search; source_conversation_id and source_session_id also work unless ambiguous."},
					"offset":    map[string]any{"type": "integer", "description": "Byte offset from next_offset_bytes for a later page; defaults to 0."},
					"max_bytes": map[string]any{"type": "integer", "description": "Maximum PKBrain JSON response size in bytes. Defaults to 16384; maximum 65536."},
				},
				"required": []string{"source_id"},
			},
		},
	}
}
