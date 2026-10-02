// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

// Cross-session retrieval: the data layer behind the three retrieval verbs.
//
// Everything in this file is READ-ONLY by construction: every statement is a
// SELECT. That is the guarantee the spec asks for -- a retrieval bug produces
// an error, never damage to chat history.
//
// Two scope rules from the spec are enforced here rather than in the HTTP
// layer, so no future caller can forget them:
//
//   - **Temporary chats are excluded from all retrieval.** They are declared
//     throwaway and are never backed up; making them recallable would make the
//     flag a lie. `Temporary` lives inside the sessions.config JSON blob, so
//     this is a json_extract filter, not a column test.
//   - **Empty and near-empty sessions are excluded from `list`.** They are
//     noise in an enumeration. `search` and `read` do not apply this: if a
//     two-message chat contains the sentence you are looking for, you want it.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// RecallDigest is one session as returned by `list`. It is the "free" tier of
// the design: title + intent + dates + counts, costing zero model calls.
type RecallDigest struct {
	SessionID    string   `json:"session_id"`
	Title        string   `json:"title"`
	Project      string   `json:"project"`
	StartedAt    string   `json:"started_at"`
	LastActiveAt string   `json:"last_active_at"`
	MessageCount int      `json:"message_count"`
	Intent       string   `json:"intent"`
	Notes        []string `json:"notes"`
	// NotesCoverThrough is the last day covered by generated notes, or "" when
	// none exist.
	NotesCoverThrough string `json:"notes_cover_through"`
	// Source is "available" while the chat exists, and "deleted" for notes
	// that outlive their session.
	Source string `json:"source"`
}

// RecallHit is one search result: a ranked snippet plus the session that owns
// it, so the user can always see where a claim came from.
type RecallHit struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Role      string `json:"role"`
	At        string `json:"at"`
	Snippet   string `json:"snippet"`
	Source    string `json:"source"`
}

// RecallMessage is one message in a `read` transcript.
type RecallMessage struct {
	Role    string `json:"role"`
	At      string `json:"at"`
	Content string `json:"content"`
}

// RecallTranscript is the `read` response.
//
// The counts are not decoration. The first agent to use `read` in anger asked
// for limit=1000 on a 434-message chat, silently received the OLDEST 200, and
// reasonably concluded that was the whole conversation -- so it could not see
// the recent activity it had been asked about. A cap is right (the newest 200
// messages of that chat are ~75k tokens, so honouring 1000 would have blown the
// context window), but a silent cap is a trap. Every response now states what
// slice of what whole it is, and Note spells it out in words.
type RecallTranscript struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Source    string `json:"source"`
	// TotalMessages is the size of the whole chat, ignoring any From/To window.
	TotalMessages int `json:"total_messages"`
	// Matched is how many messages fall inside the From/To window (equal to
	// TotalMessages when no window was given).
	Matched  int    `json:"matched"`
	Returned int    `json:"returned"`
	Offset   int    `json:"offset"`
	Order    string `json:"order"`
	// Truncated reports that messages matched the request but were not returned.
	Truncated bool `json:"truncated"`
	// RequestedLimit is echoed only when the caller's limit was reduced, so a
	// clamp is visible rather than inferred.
	RequestedLimit int `json:"requested_limit,omitempty"`
	// Note explains any capping in words, including how to reach the rest.
	Note string `json:"note,omitempty"`
	// Notes carries the day notes for a chat whose messages are gone. It is
	// what a source="deleted" response has instead of a transcript.
	Notes []string `json:"notes,omitempty"`
	// Messages is the transcript. It is always present -- as [] when a window
	// simply matched nothing -- except when source is "deleted", where
	// MarshalJSON drops the key entirely.
	Messages []RecallMessage `json:"messages"`
}

// MarshalJSON omits `messages` entirely for a deleted chat.
//
// `omitempty` cannot express this: it would also drop the key when a live chat
// has no messages in the requested window, which is a different answer ("no
// messages here") wearing the same clothes as "the chat is gone". Those two
// must not be confusable, so the distinction is made on Source rather than on
// the length of the slice.
func (t RecallTranscript) MarshalJSON() ([]byte, error) {
	type alias RecallTranscript // avoids recursing into this method
	if t.Source != sourceDeleted {
		return json.Marshal(alias(t))
	}
	return json.Marshal(struct {
		alias
		Messages []RecallMessage `json:"messages,omitempty"`
	}{alias: alias(t), Messages: nil})
}

// RecallReadOptions filters `read`.
type RecallReadOptions struct {
	SessionID string
	From      string // inclusive, YYYY-MM-DD or RFC3339; "" = unbounded
	To        string // inclusive
	Limit     int
	Offset    int
	// Order selects which END of the transcript to read from: "oldest"
	// (default) or "newest". It does NOT change the order of the returned
	// array, which is always chronological so the transcript reads naturally.
	// "newest" exists because catching up on a long chat is the common case and
	// should not require first fetching a count to compute an offset.
	Order string
}

// RecallListResult is the `list` response.
type RecallListResult struct {
	Sessions []RecallDigest `json:"sessions"`
	// Total is how many sessions matched before the limit was applied. `list`
	// silently returned 200 of 215 chats before this existed.
	Total          int    `json:"total"`
	Returned       int    `json:"returned"`
	Offset         int    `json:"offset"`
	Truncated      bool   `json:"truncated"`
	RequestedLimit int    `json:"requested_limit,omitempty"`
	Note           string `json:"note,omitempty"`
}

// RecallSearchResult is the `search` response.
type RecallSearchResult struct {
	Hits []RecallHit `json:"hits"`
	// Total is the number of matching messages; Hits holds the best-ranked
	// Returned of them. Unlike list/read this is a relevance cut rather than an
	// arbitrary window, but the caller still needs to know one was made -- and
	// needs Offset to be able to walk past it.
	Total          int    `json:"total"`
	Returned       int    `json:"returned"`
	Offset         int    `json:"offset"`
	Truncated      bool   `json:"truncated"`
	RequestedLimit int    `json:"requested_limit,omitempty"`
	Note           string `json:"note,omitempty"`
	// Match is "all" when every term had to appear in the same message, and
	// "any" when that found nothing and the terms were OR-ed instead. It is
	// always present so the caller can tell which question was actually
	// answered without having to parse the note.
	Match string `json:"match"`
	// Span is the date range of the WHOLE match set, not of the page in Hits.
	//
	// Ranking is by relevance, so a page can be entirely old while newer
	// matches sit just past the cut. The caller could see that a cut was made
	// (Total, Truncated, Note) but not which direction the rest lay in, so the
	// honest reading of a July-only page was "this was settled in July" even
	// when a September message had reversed it. Span makes that visible: a
	// Newest later than the hits in hand means re-query with from=.
	//
	// Nil when nothing matched -- there is no range to report, and an empty
	// pair of dates would read as one.
	Span *RecallSpan `json:"span,omitempty"`
}

// RecallSpan is the oldest and newest timestamp in a match set.
type RecallSpan struct {
	Oldest string `json:"oldest"`
	Newest string `json:"newest"`
}

// The values of RecallSearchResult.Match.
const (
	matchAll = "all"
	matchAny = "any"
)

// RecallListOptions filters `list`.
type RecallListOptions struct {
	From    string // inclusive, YYYY-MM-DD or RFC3339; "" = unbounded
	To      string // inclusive
	Project string // project id; "" = every project and none
	Limit   int
	Offset  int
	MinMsgs int
	// Notes selects how much note text comes back: NotesRange (the default)
	// attaches only notes inside From/To, NotesNone attaches none at all.
	//
	// NotesNone exists because there was no way to ask what happened without
	// paying for every summary of it. Sweeping a range for activity and then
	// fetching detail for the few sessions that matter is the cheap shape, and
	// it was not previously expressible.
	Notes string
}

// The values of RecallListOptions.Notes.
//
// There is deliberately no "all" mode. Attaching every note a session ever
// had, regardless of the range asked for, was the bug -- keeping it reachable
// behind a flag would preserve it as a feature and leave callers a way to
// re-acquire the problem. A caller who wants one session's whole history has
// `read`, which is the verb for that.
const (
	NotesRange = "range"
	NotesNone  = "none"
)

// RecallSearchOptions filters `search`.
type RecallSearchOptions struct {
	Query   string
	Role    string // "user" | "assistant"; "" = both
	From    string
	To      string
	Project string
	Limit   int
	// Offset pages through the ranked results. Relevance order is stable for a
	// given query, so offset walks steadily down the ranking rather than
	// reshuffling. Without it, "truncated" would be a dead end: the only advice
	// left is to narrow the query, which finds *different* matches rather than
	// the remaining ones.
	Offset int
}

const (
	// recallDefaultLimit is what every verb returns when the caller does not
	// say. It is deliberately the same for all three and deliberately NOT the
	// maximum: the default protects the careless call, the maximum bounds the
	// deliberate one, and collapsing the two (as this originally did) is what
	// made "limit=1000" look like a reasonable thing to ask for.
	recallDefaultLimit = 200

	// The ceilings differ per verb because the rows differ enormously in size
	// and, more importantly, in how bounded they are. Measured over a real
	// 206-session workspace:
	//
	//	verb     avg row   p95     max      bounded by
	//	list       610 B   926 B   995 B    intent is cut at recallIntentChars
	//	search     264 B   298 B   316 B    snippet() is a fixed window
	//	read     1,626 B 3,423 B 20,640 B   nothing -- a message is as long as it is
	//
	// So a search hit is not merely smaller than a message, it is *predictable*:
	// 1000 hits cost ~62k tokens and cannot surprise you. A read of 200 messages
	// already costs ~80k and the next one could be worse, which is why read
	// stays where it is while the cheap, bounded verbs are allowed to go further.
	recallMaxList   = 500
	recallMaxSearch = 1000
	recallMaxRead   = 200

	// recallMinMessages is the near-empty cutoff for `list`.
	recallMinMessages = 2

	// recallIntentChars bounds the "intent" excerpt. The measured average first
	// user message is 536 characters, so this keeps essentially all of a
	// typical one while bounding a pathological paste.
	recallIntentChars = 600
)

// notTemporary excludes throwaway chats. json_extract returns NULL for a
// session whose config predates the flag, and NULL is not 1, so those are
// correctly treated as non-temporary.
const notTemporary = `COALESCE(json_extract(s.config, '$.temporary'), 0) != 1`

// clampLimit applies the verb's own ceiling. An absent limit gets the shared
// default rather than the ceiling.
func clampLimit(n, max int) int {
	if n <= 0 {
		return recallDefaultLimit
	}
	if n > max {
		return max
	}
	return n
}

// clampedFrom reports the caller's original limit when it was reduced, and 0
// otherwise, so a response can show a clamp instead of hiding it.
func clampedFrom(requested, max int) int {
	if requested > max {
		return requested
	}
	return 0
}

func clampOffset(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// normalizeDay turns a user-supplied bound into something SQLite's string
// comparison handles correctly against `created_at`.
//
// A bare date is expanded rather than compared directly: `to=2026-09-08` must
// mean "through the end of that day", and comparing against the bare string
// would silently exclude everything after midnight -- an off-by-one-day error
// that would look like missing data rather than a bug.
func normalizeDay(v string, endOfDay bool) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if len(v) == 10 {
		if _, err := time.Parse("2006-01-02", v); err == nil {
			if endOfDay {
				return v + " 23:59:59"
			}
			return v + " 00:00:00"
		}
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC().Format("2006-01-02 15:04:05")
	}
	return v
}

// dayOf reduces a from/to parameter to the 'YYYY-MM-DD' the session_notes.day
// column stores, so a range expressed in either accepted format can be
// compared against it. Empty in, empty out -- an unbounded end stays unbounded.
func dayOf(v string) string {
	n := normalizeDay(v, false)
	if len(n) < 10 {
		return ""
	}
	return n[:10]
}

// labelOf pulls the display title out of the config blob. The title is not a
// column -- it lives inside sessions.config as JSON.
func labelOf(cfgJSON string) string {
	var cfg struct {
		Label string `json:"label"`
	}
	json.Unmarshal([]byte(cfgJSON), &cfg)
	if s := strings.TrimSpace(cfg.Label); s != "" {
		return s
	}
	return "(untitled)"
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary so a multi-byte character is never split.
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// RecallList enumerates sessions in a time window as cheap digests.
func RecallList(opt RecallListOptions) (RecallListResult, error) {
	limit := clampLimit(opt.Limit, recallMaxList)
	offset := clampOffset(opt.Offset)
	minMsgs := opt.MinMsgs
	if minMsgs <= 0 {
		minMsgs = recallMinMessages
	}

	where := []string{notTemporary}
	args := []any{}
	if from := normalizeDay(opt.From, false); from != "" {
		where = append(where, `datetime(s.updated_at) >= datetime(?)`)
		args = append(args, from)
	}
	if to := normalizeDay(opt.To, true); to != "" {
		where = append(where, `datetime(s.created_at) <= datetime(?)`)
		args = append(args, to)
	}
	if p := strings.TrimSpace(opt.Project); p != "" {
		where = append(where, `s.project_id = ?`)
		args = append(args, p)
	}
	clause := strings.Join(where, " AND ")

	// Deleted-but-noted chats are enumerated alongside live ones.
	// Notes outlive their session precisely so that tidying the sidebar
	// does not punch a hole in the record; a note nothing can enumerate would
	// only be reachable by already knowing the session id, which defeats that.
	//
	// The two sources are unioned in SQL rather than merged in Go so that
	// ordering, counting and paging are computed over the whole sequence. A
	// live page fetched separately and then concatenated would silently
	// mis-order across the page boundary and make `total` disagree with what
	// paging actually walks.
	orphanWhere, orphanArgs := orphanedListClause(opt)

	entries := `
		SELECT id, cfg, plain_title, project, started, last_active, msg_count, intent, source FROM (
			SELECT s.id AS id, s.config AS cfg, '' AS plain_title,
			       COALESCE(p.name, '') AS project,
			       ` + rfc3339("s.created_at") + ` AS started,
			       ` + rfc3339("s.updated_at") + ` AS last_active,
			       (SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id) AS msg_count,
			       COALESCE((SELECT m2.content FROM messages m2
			                 WHERE m2.session_id = s.id AND m2.role = 'user'
			                 ORDER BY m2.id ASC LIMIT 1), '') AS intent,
			       '` + sourceAvailable + `' AS source
			FROM sessions s
			LEFT JOIN projects p ON p.id = s.project_id
			WHERE ` + clause + `
			GROUP BY s.id
			HAVING msg_count >= ?

			UNION ALL

			SELECT n.session_id, NULL, COALESCE(MAX(n.session_title), ''),
			       COALESCE(MAX(p.name), ''),
			       ` + rfc3339("MIN(n.day)") + `, ` + rfc3339("MAX(n.day)") + `, 0, '',
			       '` + sourceDeleted + `'
			FROM session_notes n
			LEFT JOIN projects p ON p.id = n.project_id
			WHERE ` + orphanWhere + `
			GROUP BY n.session_id
		)`

	// Count first, so the caller can tell a page from the whole.
	var total int
	countArgs := append(append([]any{}, args...), minMsgs)
	countArgs = append(countArgs, orphanArgs...)
	if err := DB.QueryRow(`SELECT COUNT(*) FROM (`+entries+`)`, countArgs...).Scan(&total); err != nil {
		return RecallListResult{}, fmt.Errorf("recall list count: %w", err)
	}

	q := entries + `
		ORDER BY datetime(last_active) DESC
		LIMIT ? OFFSET ?`
	args = append(args, minMsgs)
	args = append(args, orphanArgs...)
	args = append(args, limit, offset)

	rows, err := DB.Query(q, args...)
	if err != nil {
		return RecallListResult{}, fmt.Errorf("recall list: %w", err)
	}
	defer rows.Close()

	out := []RecallDigest{}
	for rows.Next() {
		var id, plainTitle, project, started, lastActive, intent, source string
		var cfgJSON sql.NullString
		var count int
		if err := rows.Scan(&id, &cfgJSON, &plainTitle, &project, &started, &lastActive,
			&count, &intent, &source); err != nil {
			return RecallListResult{}, fmt.Errorf("recall list scan: %w", err)
		}
		// A deleted chat has no config blob left to read a label out of, which
		// is exactly why the title was snapshotted onto the note at write time.
		title := plainTitle
		if cfgJSON.Valid {
			title = labelOf(cfgJSON.String)
		}
		out = append(out, RecallDigest{
			SessionID:    id,
			Title:        title,
			Project:      project,
			StartedAt:    started,
			LastActiveAt: lastActive,
			MessageCount: count,
			Intent:       truncate(intent, recallIntentChars),
			// Populated below from session_notes in one batched query rather
			// than joined here: a session has many notes, and joining would
			// multiply the digest rows.
			Notes:             []string{},
			NotesCoverThrough: "",
			Source:            source,
		})
	}
	if err := rows.Err(); err != nil {
		return RecallListResult{}, err
	}

	if err := attachNotes(out, dayOf(opt.From), dayOf(opt.To), opt.Notes != NotesNone); err != nil {
		return RecallListResult{}, err
	}

	res := RecallListResult{
		Sessions:       out,
		Total:          total,
		Returned:       len(out),
		Offset:         offset,
		Truncated:      offset+len(out) < total,
		RequestedLimit: clampedFrom(opt.Limit, recallMaxList),
	}
	if res.Truncated {
		res.Note = fmt.Sprintf(
			"Showing sessions %d-%d of %d. Pass offset=%d for the next page, or narrow with from/to.",
			offset+1, offset+len(out), total, offset+len(out))
		if res.RequestedLimit > 0 {
			res.Note = fmt.Sprintf("Limit reduced from %d to the %d maximum. ", res.RequestedLimit, recallMaxList) + res.Note
		}
	}
	return res, nil
}

// RecallSearch runs a ranked full-text query over message content.
//
// Relevance ordering is the reason this exists: a plain LIKE with LIMIT 50 and
// no ORDER BY returns whichever 50 rows the scan reached first, not the 50 best.
func RecallSearch(opt RecallSearchOptions) (RecallSearchResult, error) {
	query := strings.TrimSpace(opt.Query)
	if query == "" {
		return RecallSearchResult{Hits: []RecallHit{}}, nil
	}
	if !FTSReady() {
		return RecallSearchResult{}, fmt.Errorf("search index unavailable")
	}
	limit := clampLimit(opt.Limit, recallMaxSearch)
	offset := clampOffset(opt.Offset)

	where := []string{`messages_fts MATCH ?`, notTemporary}
	args := []any{ftsQuery(query)}
	if role := strings.TrimSpace(opt.Role); role != "" {
		where = append(where, `m.role = ?`)
		args = append(args, role)
	}
	if from := normalizeDay(opt.From, false); from != "" {
		where = append(where, `datetime(m.created_at) >= datetime(?)`)
		args = append(args, from)
	}
	if to := normalizeDay(opt.To, true); to != "" {
		where = append(where, `datetime(m.created_at) <= datetime(?)`)
		args = append(args, to)
	}
	if p := strings.TrimSpace(opt.Project); p != "" {
		where = append(where, `s.project_id = ?`)
		args = append(args, p)
	}
	clause := strings.Join(where, " AND ")

	from := `
		FROM messages_fts
		JOIN messages m ON m.id = messages_fts.rowid
		JOIN sessions s ON s.id = m.session_id
		WHERE ` + clause

	total, span, err := countAndSpan(from, args)
	if err != nil {
		// An unparseable FTS5 expression lands here. Report it as a bad query
		// rather than an internal failure, so the agent can retry differently.
		return RecallSearchResult{}, fmt.Errorf("recall search: %w", err)
	}

	// Every token must appear in the same message, so each extra word makes
	// the query strictly narrower -- the exact opposite of the natural reading,
	// where describing what you want more fully ought to help. An agent asking
	// a real question in eight words gets nothing back, and nothing
	// distinguishes that from "this was never discussed": the failure is
	// silent, and silence here produces false confidence.
	//
	// So a multi-word query that matched nothing is retried with the tokens
	// OR-ed, and the reply says so. A flag would only help a caller who
	// already knew about the AND, and a caller who knew would have written a
	// better query in the first place. This costs one extra count only in the
	// case that currently fails, and changes nothing when the query works.
	match := matchAll
	if total == 0 && len(ftsTokens(query)) > 1 {
		anyArgs := append([]any{}, args...)
		anyArgs[0] = ftsQueryAny(query)
		// The span must be recomputed here, not carried over. The ALL count
		// that got us into this branch was zero, so its span describes an
		// empty set; leaving it in place would put a span next to a total and
		// a page that describe a different set entirely.
		anyTotal, anySpan, err := countAndSpan(from, anyArgs)
		if err == nil && anyTotal > 0 {
			match, total, span, args = matchAny, anyTotal, anySpan, anyArgs
		}
	}

	q := `
		SELECT m.session_id, s.config, m.role, m.created_at,
		       snippet(messages_fts, 0, '[', ']', '…', 12) ` + from + `
		ORDER BY rank
		LIMIT ? OFFSET ?`

	rows, err := DB.Query(q, append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return RecallSearchResult{}, fmt.Errorf("recall search: %w", err)
	}
	defer rows.Close()

	out := []RecallHit{}
	for rows.Next() {
		var sid, cfgJSON, role, at, snip string
		if err := rows.Scan(&sid, &cfgJSON, &role, &at, &snip); err != nil {
			return RecallSearchResult{}, fmt.Errorf("recall search scan: %w", err)
		}
		out = append(out, RecallHit{
			SessionID: sid,
			Title:     labelOf(cfgJSON),
			Role:      role,
			At:        at,
			Snippet:   snip,
			Source:    "available",
		})
	}
	if err := rows.Err(); err != nil {
		return RecallSearchResult{}, err
	}

	res := RecallSearchResult{
		Hits:           out,
		Total:          total,
		Returned:       len(out),
		Offset:         offset,
		Truncated:      offset+len(out) < total,
		RequestedLimit: clampedFrom(opt.Limit, recallMaxSearch),
		Match:          match,
		Span:           span,
	}
	if res.Truncated {
		res.Note = fmt.Sprintf(
			"Showing matches %d-%d of %d, best-ranked first. Pass offset=%d for the next page, "+
				"or narrow with from/to, role or project.",
			offset+1, offset+len(out), total, offset+len(out))
		if res.RequestedLimit > 0 {
			res.Note = fmt.Sprintf("Limit reduced from %d to the %d maximum. ", res.RequestedLimit, recallMaxSearch) + res.Note
		}
	}
	if match == matchAny {
		// Stated in words, and first. The agent has to know these hits answer a
		// weaker question than the one it asked, or it will read "5 results"
		// as "5 messages about all of this" -- which is the same
		// false-confidence failure the fallback exists to prevent, arriving by
		// a different route.
		res.Note = strings.TrimSpace(fmt.Sprintf(
			"No message contained all %d terms, so these are ranked matches for ANY of them "+
				"and each hit may cover only part of the query. Narrow to the key words for a stricter search. %s",
			len(ftsTokens(query)), res.Note))
	}
	return res, nil
}

// countAndSpan sizes a match set and reports its date range in one statement.
//
// The span is free in query terms -- COUNT already scans the whole match set,
// so MIN/MAX ride along on the same scan rather than costing a second query.
// It is not literally free in time (measured at +14-21% on the count), but the
// count is sub-millisecond outside pathological queries, so the alternative --
// leaving the caller unable to tell which direction the unreturned matches lie
// in -- is much the worse trade.
//
// MIN/MAX are wrapped in rfc3339 rather than returned raw. An aggregate loses
// the column's declared type, so the driver stops converting the value and
// hands back SQLite's storage format ("2026-07-13 11:19:11") instead of the
// RFC3339 every other timestamp in this package uses. A span in a different
// format from the hit timestamps it exists to be compared against is worse
// than no span at all, and this is the second time that conversion has bitten.
func countAndSpan(from string, args []any) (int, *RecallSpan, error) {
	var total int
	var oldest, newest sql.NullString
	err := DB.QueryRow(
		`SELECT COUNT(*), `+rfc3339(`MIN(m.created_at)`)+`, `+rfc3339(`MAX(m.created_at)`)+` `+from,
		args...,
	).Scan(&total, &oldest, &newest)
	if err != nil {
		return 0, nil, err
	}
	// An empty match set aggregates to NULL, which is the honest answer: there
	// is no range. Reporting two empty strings would look like one.
	if !oldest.Valid || !newest.Valid {
		return total, nil, nil
	}
	return total, &RecallSpan{Oldest: oldest.String, Newest: newest.String}, nil
}

// ftsQuery makes a user's words safe to hand to FTS5.
//
// FTS5 MATCH is an expression language, not a literal: bare input containing
// a colon, a dash or a quote is either a syntax error or -- worse -- silently
// means something else (`foo:bar` is a column filter, `-foo` is a negation).
// An agent passing a natural phrase must not have to know that. Each token is
// therefore quoted as a literal, which preserves phrase and multi-word search
// while removing every operator. A trailing * is kept, because prefix search
// is genuinely useful and is the one operator worth exposing.
func ftsQuery(q string) string {
	parts := ftsTokens(q)
	if len(parts) == 0 {
		// Nothing survived sanitising (e.g. the query was only quotes). Return
		// a literal that matches nothing rather than an empty string, which
		// FTS5 rejects as a syntax error.
		return `""`
	}
	return strings.Join(parts, " ")
}

// ftsQueryAny is ftsQuery with the tokens OR-ed instead of AND-ed. It is only
// ever used as a fallback after the AND form matched nothing -- see
// RecallSearch. bm25 ranking is what makes it useful rather than noise, and
// FTS5 has no stopword list, so on a query full of common words this returns a
// great many weak matches. That is acceptable as a second attempt at zero hits
// and would not be acceptable as a default.
func ftsQueryAny(q string) string {
	parts := ftsTokens(q)
	if len(parts) == 0 {
		return `""`
	}
	return strings.Join(parts, " OR ")
}

// ftsTokens sanitises a user query into quoted FTS5 tokens. Quoting each one
// neutralises the operators a natural question stumbles into -- bare `foo:bar`
// is a column filter and `-foo` is a negation -- so the words are matched as
// words.
func ftsTokens(q string) []string {
	fields := strings.Fields(q)
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		prefix := false
		if strings.HasSuffix(f, "*") && len(f) > 1 {
			prefix = true
			f = strings.TrimSuffix(f, "*")
		}
		f = strings.ReplaceAll(f, `"`, `""`)
		if f == "" {
			continue
		}
		if prefix {
			parts = append(parts, `"`+f+`"*`)
		} else {
			parts = append(parts, `"`+f+`"`)
		}
	}
	return parts
}

// RecallRead returns a slice of a transcript, always saying which slice.
//
// Order selects which end to read from; the returned array is always
// chronological, because a transcript read backwards is not a transcript.
// Offset counts from the chosen end, so order="newest" with offset=50 is the
// 50 messages before the last 50.
func RecallRead(opt RecallReadOptions) (RecallTranscript, error) {
	limit := clampLimit(opt.Limit, recallMaxRead)
	offset := clampOffset(opt.Offset)
	order := strings.ToLower(strings.TrimSpace(opt.Order))
	if order != "newest" {
		order = "oldest"
	}

	var cfgJSON string
	err := DB.QueryRow(
		`SELECT config FROM sessions WHERE id = ? AND `+notTemporaryBare, opt.SessionID,
	).Scan(&cfgJSON)
	if err != nil {
		// The chat may still be *known* through its notes, which outlive it.
		// Answering that case with an empty
		// messages array would read as "the chat was empty" rather than "the
		// chat is gone", so it gets an explicit response instead.
		if tr, ok := deletedTranscript(opt.SessionID); ok {
			return tr, nil
		}
		// Either no such session, or a Temporary one -- deliberately
		// indistinguishable, so retrieval cannot be used to probe for the
		// existence of a chat it is not allowed to read.
		return RecallTranscript{}, fmt.Errorf("session not found: %s", opt.SessionID)
	}

	var total int
	if err := DB.QueryRow(
		`SELECT COUNT(*) FROM messages WHERE session_id = ?`, opt.SessionID,
	).Scan(&total); err != nil {
		return RecallTranscript{}, fmt.Errorf("recall read count: %w", err)
	}

	where := []string{`session_id = ?`}
	args := []any{opt.SessionID}
	if from := normalizeDay(opt.From, false); from != "" {
		where = append(where, `datetime(created_at) >= datetime(?)`)
		args = append(args, from)
	}
	if to := normalizeDay(opt.To, true); to != "" {
		where = append(where, `datetime(created_at) <= datetime(?)`)
		args = append(args, to)
	}
	clause := strings.Join(where, " AND ")

	matched := total
	windowed := len(where) > 1
	if windowed {
		if err := DB.QueryRow(
			`SELECT COUNT(*) FROM messages WHERE `+clause, args...,
		).Scan(&matched); err != nil {
			return RecallTranscript{}, fmt.Errorf("recall read match count: %w", err)
		}
	}

	dir := "ASC"
	if order == "newest" {
		dir = "DESC"
	}
	rows, err := DB.Query(
		`SELECT role, created_at, content FROM messages
		 WHERE `+clause+` ORDER BY id `+dir+` LIMIT ? OFFSET ?`,
		append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return RecallTranscript{}, fmt.Errorf("recall read: %w", err)
	}
	defer rows.Close()

	msgs := []RecallMessage{}
	for rows.Next() {
		var role, at, content string
		if err := rows.Scan(&role, &at, &content); err != nil {
			return RecallTranscript{}, fmt.Errorf("recall read scan: %w", err)
		}
		msgs = append(msgs, RecallMessage{Role: role, At: at, Content: content})
	}
	if err := rows.Err(); err != nil {
		return RecallTranscript{}, err
	}
	if order == "newest" {
		// Selected from the end, but handed back in reading order.
		for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
			msgs[i], msgs[j] = msgs[j], msgs[i]
		}
	}

	tr := RecallTranscript{
		SessionID:      opt.SessionID,
		Title:          labelOf(cfgJSON),
		Source:         "available",
		TotalMessages:  total,
		Matched:        matched,
		Returned:       len(msgs),
		Offset:         offset,
		Order:          order,
		Truncated:      offset+len(msgs) < matched,
		RequestedLimit: clampedFrom(opt.Limit, recallMaxRead),
		Messages:       msgs,
	}
	tr.Note = readNote(tr, windowed)
	return tr, nil
}

// readNote states in words what slice of the chat came back, and how to get the
// rest. The numeric fields alone were not enough: an agent that asked for 1000
// and got 200 had every number it needed to notice, and still concluded it had
// read the whole conversation.
func readNote(tr RecallTranscript, windowed bool) string {
	var parts []string
	if tr.RequestedLimit > 0 {
		parts = append(parts, fmt.Sprintf(
			"Limit reduced from %d to %d (a %d-message slice can be very large).",
			tr.RequestedLimit, recallMaxRead, recallMaxRead))
	}
	if windowed && tr.Matched < tr.TotalMessages {
		parts = append(parts, fmt.Sprintf(
			"%d of %d messages fall in the requested date range.", tr.Matched, tr.TotalMessages))
	}
	if tr.Truncated {
		end := "oldest"
		if tr.Order == "newest" {
			end = "newest"
		}
		parts = append(parts, fmt.Sprintf(
			"Showing %d %s messages (offset %d) of %d. This is NOT the whole chat: pass offset=%d for the next slice, order=%q to read from the other end, or from/to to narrow by date.",
			tr.Returned, end, tr.Offset, tr.Matched, tr.Offset+tr.Returned,
			map[string]string{"oldest": "newest", "newest": "oldest"}[tr.Order]))
	}
	return strings.Join(parts, " ")
}

// notTemporaryBare is notTemporary without the `s.` alias, for queries that
// select from `sessions` without aliasing it.
const notTemporaryBare = `COALESCE(json_extract(config, '$.temporary'), 0) != 1`
