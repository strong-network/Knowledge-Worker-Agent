// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcprecall

// Tool definitions.
//
// These descriptions are the entire discovery mechanism. MCP was chosen over a
// CLI subcommand precisely because the agent sees this list natively in every
// session -- so the text here is load-bearing product surface, not
// documentation. Two things it must do:
//
//   - say *when* to reach for the tool, not just what it does, because the
//     agent's problem is knowing that past conversations exist at all;
//   - steer towards the cheap verb first. `list` and `search` cost a query;
//     `read` can pull a very large transcript. The two-stage shape
//     (enumerate cheaply, then read selectively) only happens if the
//     descriptions ask for it.

func toolDefs() []any {
	return []any{
		map[string]any{
			"name": "recall_search",
			"description": "Search the full history of this user's past chat sessions by keyword " +
				"or phrase, ranked by relevance. Use this whenever the user refers to an earlier " +
				"conversation (\"we discussed this before\", \"what did I decide about X\", \"have I " +
				"asked you this already\"), or when you suspect prior context exists that this " +
				"session cannot see. Returns short snippets plus the session each came from; " +
				"follow up with recall_read on a session that looks right. " +
				"The reply reports 'total' and 'truncated': if truncated, you have the best-ranked " +
				"page only -- pass offset to walk further down the ranking. " +
				"It also reports 'span', the dates of the oldest and newest matching message " +
				"anywhere in the result set, not just on your page. Hits are ranked by relevance, " +
				"not date, so check it: if span.newest is later than the newest hit you were given, " +
				"more recent material exists that you have not seen -- repeat the search with " +
				"from= set near that date before concluding anything about what was decided.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type": "string",
						"description": "Words to find. ALL of them must appear in the SAME message, so each " +
							"extra word NARROWS the search -- a long natural-language question will " +
							"usually match nothing. Prefer two or three distinctive terms. " +
							"Append * to a word for prefix search. " +
							"If nothing matches all the terms, the search is automatically retried " +
							"for ANY of them and the reply comes back with match=\"any\": those hits " +
							"each answer only part of what you asked, so check them before relying on them.",
					},
					"role": map[string]any{
						"type": "string",
						"enum": []string{"user", "assistant"},
						"description": "Restrict to what the user asked, or what the assistant said. " +
							"These give genuinely different answers: assistant text is roughly 8x the " +
							"volume of user text, so unfiltered results are dominated by replies.",
					},
					"from":    map[string]any{"type": "string", "description": "Earliest date, YYYY-MM-DD."},
					"to":      map[string]any{"type": "string", "description": "Latest date, YYYY-MM-DD."},
					"project": map[string]any{"type": "string", "description": "Restrict to one project id."},
					"limit":   map[string]any{"type": "integer", "description": "Max hits to return. Defaults to 200; the maximum is 1000 and larger values are reduced."},
					"offset":  map[string]any{"type": "integer", "description": "Skip this many hits, to page further down the ranking. Use with 'total' in the reply."},
				},
				"required": []string{"query"},
			},
		},
		map[string]any{
			"name": "recall_list",
			"description": "List past chat sessions in a date range as cheap summaries (title, the " +
				"user's opening request, dates, message count). Use this for questions about a " +
				"period rather than a keyword -- \"what did I work on last week\", \"what has been " +
				"going on with this project\" -- where there is no obvious term to search for. " +
				"Costs nothing but a query, so prefer it over reading transcripts. The reply " +
				"reports 'total' and 'truncated': if truncated, you have one page, not the whole list. " +
				"An entry may also carry 'notes': dated one-per-day summaries of what actually " +
				"happened in that chat. Prefer them over the opening request for any chat that ran " +
				"long or across several days, where the first message stops describing what the " +
				"chat became. Notes are limited to the from/to range you asked for, so a chat that " +
				"started months ago contributes only the days in your window. " +
				"'notes_cover_through' is the last day summarised -- it reports the chat's LAST " +
				"note, in or out of your range, so it stays a true high-water mark: activity after " +
				"that date is NOT covered by the notes, so do not read their silence as nothing " +
				"having happened. An entry with source=\"deleted\" is a chat the user removed; " +
				"its notes survive but its messages cannot be read.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"from":    map[string]any{"type": "string", "description": "Earliest date, YYYY-MM-DD."},
					"to":      map[string]any{"type": "string", "description": "Latest date, YYYY-MM-DD."},
					"project": map[string]any{"type": "string", "description": "Restrict to one project id."},
					"limit":   map[string]any{"type": "integer", "description": "Max sessions to return. Defaults to 200; the maximum is 500 and larger values are reduced."},
					"offset":  map[string]any{"type": "integer", "description": "Skip this many sessions. Use with the 'total' in the reply to page through the rest."},
					"min_messages": map[string]any{
						"type": "integer",
						"description": "Only return sessions with at least this many messages. Useful for " +
							"skipping abandoned one-question chats when summarising a period.",
					},
					"notes": map[string]any{
						"type": "string",
						"enum": []string{"range", "none"},
						"description": "How much note text to attach. \"range\" (the default) attaches only " +
							"notes inside from/to. \"none\" attaches no note text at all, which makes " +
							"this a cheap sweep for activity -- follow up on the sessions that look " +
							"relevant. 'notes_cover_through' is still reported either way.",
					},
				},
			},
		},
		map[string]any{
			"name": "recall_read",
			"description": "Read messages from one past chat session, given its id from " +
				"recall_search or recall_list. This is the expensive verb: narrow down first " +
				"and read only the session that matters. " +
				"A LONG CHAT IS NEVER RETURNED WHOLE -- at most 200 messages come back per call, " +
				"and by default those are the OLDEST 200, which for an active session is its " +
				"beginning, not its current state. To catch up on what happened recently, pass " +
				"order=\"newest\", or from/to for a date range -- do NOT just raise limit, which " +
				"is capped. Always check 'total_messages', 'truncated' and 'note' in the reply " +
				"before concluding you have seen the whole conversation. " +
				"If the reply has source=\"deleted\" there is no 'messages' field at all: the " +
				"user deleted that chat and only its day 'notes' survive. Say so rather than " +
				"quoting the notes as if they were the conversation.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session_id": map[string]any{
						"type":        "string",
						"description": "Session id, as returned by recall_search or recall_list.",
					},
					"order": map[string]any{
						"type": "string",
						"enum": []string{"oldest", "newest"},
						"description": "Which end of the chat to read from. \"oldest\" (default) starts at " +
							"the beginning; \"newest\" returns the most recent messages, which is what you " +
							"want for \"what happened lately\". Messages always come back in chronological " +
							"order either way.",
					},
					"from": map[string]any{"type": "string", "description": "Only messages on or after this date, YYYY-MM-DD. Use this for \"the last N days\"."},
					"to":   map[string]any{"type": "string", "description": "Only messages on or before this date, YYYY-MM-DD."},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Max messages to return. Defaults to 200, which is also the maximum here (messages are unbounded in size, unlike search hits); larger values are reduced, not honoured.",
					},
					"offset": map[string]any{
						"type": "integer",
						"description": "Skip this many messages from the chosen end, to page through a long chat. " +
							"With order=\"newest\", offset=200 is the 200 messages before the most recent 200.",
					},
				},
				"required": []string{"session_id"},
			},
		},
	}
}
