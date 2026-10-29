// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Chat search, for the Search page: finds chats by their titles and by what was
// said in them, one result per chat, best first. Unlike RecallSearch, which
// serves the agent, it includes temporary chats, matches the start of words and
// allows for typos. Matches without a typo always rank above those with one.

// ChatSearchOptions is a Search page query.
type ChatSearchOptions struct {
	Query  string
	From   string // inclusive, YYYY-MM-DD or RFC 3339; "" = unbounded
	To     string
	Limit  int
	Offset int
}

// ChatSearchResult is a page of matching chats.
type ChatSearchResult struct {
	Chats  []ChatSearchHit `json:"chats"`
	Total  int             `json:"total"`
	Offset int             `json:"offset"`
	// Match is "any" when no chat had every word, so these have some of them.
	Match string `json:"match"`
	// Terms are the words searched for, and Variants the spellings found for
	// them allowing for typos, so the client can highlight both.
	Terms    []SearchTerm `json:"terms"`
	Variants []string     `json:"variants"`
}

// SearchTerm is a word searched for. A prefix term also matches longer words.
type SearchTerm struct {
	Text   string `json:"text"`
	Prefix bool   `json:"prefix,omitempty"`
}

// ChatSearchHit is a chat that matched.
type ChatSearchHit struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Project   string `json:"project,omitempty"`
	Temporary bool   `json:"temporary,omitempty"`
	UpdatedAt string `json:"updated_at"`
	// Exact is false when the chat matched only allowing for typos.
	Exact      bool `json:"exact"`
	TitleMatch bool `json:"title_match,omitempty"`
	// Matches counts the chat's messages that matched, at the Exact level.
	Matches int                `json:"matches"`
	Message *ChatSearchMessage `json:"message,omitempty"`
	// Excerpt is the chat's first message, when only its title matched.
	Excerpt string `json:"excerpt,omitempty"`
}

// ChatSearchMessage is a chat's best matching message.
type ChatSearchMessage struct {
	ID         int64  `json:"id"`
	Role       string `json:"role"`
	AuthorName string `json:"author_name,omitempty"`
	CreatedAt  string `json:"created_at"`
	Snippet    string `json:"snippet"`
}

const (
	chatSearchDefaultLimit = 20
	chatSearchMaxLimit     = 100
	chatSearchMaxParts     = 12
	chatSearchMaxVariants  = 8
	chatSearchExcerptChars = 200
)

// searchPart is a word of the query, or a quoted phrase, matched as typed.
type searchPart struct {
	words    []string
	phrase   bool
	variants []string
}

var quotedPhrase = regexp.MustCompile(`"([^"]*)"`)

func parseChatQuery(q string) []searchPart {
	var parts []searchPart
	seen := map[string]bool{}
	add := func(p searchPart) {
		key := fmt.Sprint(p.phrase, p.words)
		if seen[key] || len(parts) >= chatSearchMaxParts {
			return
		}
		seen[key] = true
		parts = append(parts, p)
	}
	for _, m := range quotedPhrase.FindAllStringSubmatch(q, -1) {
		if ws := searchWords(m[1]); len(ws) > 0 {
			add(searchPart{words: ws, phrase: true})
		}
	}
	for _, w := range searchWords(quotedPhrase.ReplaceAllString(q, " ")) {
		add(searchPart{words: []string{w}})
	}
	return parts
}

// searchWords splits text into lowercase words as the index does: runs of
// letters and digits. Anything else, FTS5 operators included, separates them.
func searchWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.Is(unicode.Co, r)
	})
}

// prefix: a word of three letters or more also finds longer words it starts.
func (p searchPart) prefix() bool {
	return !p.phrase && utf8.RuneCountInString(p.words[0]) >= 3
}

// strict is the part's FTS5 expression without typo variants.
func (p searchPart) strict() string {
	s := `"` + strings.Join(p.words, " ") + `"`
	if p.prefix() {
		s += "*"
	}
	return s
}

func (p searchPart) fuzzy() string {
	if len(p.variants) == 0 {
		return p.strict()
	}
	alts := []string{p.strict()}
	for _, v := range p.variants {
		alts = append(alts, `"`+v+`"`)
	}
	return "(" + strings.Join(alts, " OR ") + ")"
}

func joinParts(parts []searchPart, op string, expr func(searchPart) string) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = expr(p)
	}
	return strings.Join(out, " "+op+" ")
}

// inWords reports whether the part is among a title's words: exactly, or only
// allowing for typos.
func (p searchPart) inWords(words []string) (exact, typo bool) {
	if p.phrase {
		for i := 0; i+len(p.words) <= len(words); i++ {
			if slices.Equal(words[i:i+len(p.words)], p.words) {
				return true, false
			}
		}
		return false, false
	}
	w := p.words[0]
	for _, t := range words {
		if t == w || (p.prefix() && strings.HasPrefix(t, w)) {
			return true, false
		}
	}
	if k := maxTypos(w); k > 0 {
		wr := []rune(w)
		for _, t := range words {
			if editDistance(wr, []rune(t), k) <= k {
				return false, true
			}
		}
	}
	return false, false
}

// maxTypos is how many edits away a variant of a word may be. Short words get
// none: nearly every short word is one edit from several others.
func maxTypos(w string) int {
	switch n := utf8.RuneCountInString(w); {
	case n >= 8:
		return 2
	case n >= 4:
		return 1
	}
	return 0
}

type vocabTerm struct {
	text  string
	runes []rune
	docs  int
}

// The index's vocabulary, where typo variants come from. Reading it takes tens
// of milliseconds, so it's kept until messages are added or deleted.
var vocab struct {
	sync.Mutex
	key   string
	terms []vocabTerm
}

func vocabulary() ([]vocabTerm, error) {
	var n int
	var last sql.NullInt64
	if err := DB.QueryRow(`SELECT COUNT(*), MAX(id) FROM messages`).Scan(&n, &last); err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%p/%d/%d", DB, n, last.Int64)
	vocab.Lock()
	defer vocab.Unlock()
	if vocab.key == key {
		return vocab.terms, nil
	}
	rows, err := DB.Query(`SELECT term, doc FROM messages_fts_v`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var terms []vocabTerm
	for rows.Next() {
		var t vocabTerm
		if err := rows.Scan(&t.text, &t.docs); err != nil {
			return nil, err
		}
		t.runes = []rune(t.text)
		terms = append(terms, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	vocab.key, vocab.terms = key, terms
	return terms, nil
}

// typoVariants finds the words in the index a few edits away from w, closest
// and most used first. Words that start with w are left out: the prefix
// search finds them already.
func typoVariants(w string, terms []vocabTerm) []string {
	k := maxTypos(w)
	if k == 0 || strings.IndexFunc(w, unicode.IsDigit) >= 0 {
		return nil
	}
	wr := []rune(w)
	type candidate struct {
		text       string
		dist, docs int
	}
	var cs []candidate
	for _, t := range terms {
		if d := len(t.runes) - len(wr); d > k || d < -k {
			continue
		}
		if strings.HasPrefix(t.text, w) {
			continue
		}
		if d := editDistance(wr, t.runes, k); d <= k {
			cs = append(cs, candidate{t.text, d, t.docs})
		}
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].dist != cs[j].dist {
			return cs[i].dist < cs[j].dist
		}
		if cs[i].docs != cs[j].docs {
			return cs[i].docs > cs[j].docs
		}
		return cs[i].text < cs[j].text
	})
	out := []string{}
	for _, c := range cs {
		if len(out) == chatSearchMaxVariants {
			break
		}
		out = append(out, c.text)
	}
	return out
}

// editDistance counts the insertions, deletions, substitutions and swaps of
// neighbouring letters between a and b. It gives up at max+1.
func editDistance(a, b []rune, max int) int {
	prev2 := make([]int, len(b)+1)
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		low := i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
			low = min(low, cur[j])
		}
		if low > max {
			return max + 1
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return min(prev[len(b)], max+1)
}

type chatMeta struct {
	title, project, updated string
	temporary, inRange      bool
	messages                int
}

// chatMetas reads every chat's title and dates. inRange: the chat was active
// at some point between from and to.
func chatMetas(from, to string) (map[string]chatMeta, error) {
	rows, err := DB.Query(`
		SELECT s.id, s.config, COALESCE(p.name, ''), COALESCE(`+rfc3339("s.updated_at")+`, ''),
		       (SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id),
		       (? = '' OR datetime(s.updated_at) >= datetime(?)) AND (? = '' OR datetime(s.created_at) <= datetime(?))
		FROM sessions s
		LEFT JOIN projects p ON p.id = s.project_id`, from, from, to, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]chatMeta{}
	for rows.Next() {
		var id, cfgJSON string
		var m chatMeta
		if err := rows.Scan(&id, &cfgJSON, &m.project, &m.updated, &m.messages, &m.inRange); err != nil {
			return nil, err
		}
		var cfg struct {
			Label     string `json:"label"`
			Temporary bool   `json:"temporary"`
		}
		json.Unmarshal([]byte(cfgJSON), &cfg)
		m.title, m.temporary = strings.TrimSpace(cfg.Label), cfg.Temporary
		out[id] = m
	}
	return out, rows.Err()
}

type messageMatch struct {
	id     int64
	score  float64
	strict bool
}

type chatCandidate struct {
	session     string
	best        *messageMatch
	strictCount int
	count       int
	titleExact  bool
	titleTypo   bool
	updated     string
	strictExpr  string
	fuzzyExpr   string
}

func (c *chatCandidate) exact() bool { return c.strictCount > 0 || c.titleExact }

func (c *chatCandidate) titleHit() bool {
	if c.exact() {
		return c.titleExact
	}
	return c.titleTypo
}

// matchChats finds the chats that match every part, or with anyPart, one of them.
func matchChats(parts []searchPart, anyPart bool, from, to string, metas map[string]chatMeta) ([]*chatCandidate, error) {
	op := "AND"
	if anyPart {
		op = "OR"
	}
	strictQ := joinParts(parts, op, searchPart.strict)
	fuzzyQ := joinParts(parts, op, searchPart.fuzzy)

	strictCol, args := "1", []any{}
	if strictQ != fuzzyQ {
		strictCol = `m.id IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)`
		args = append(args, strictQ)
	}
	where := []string{`messages_fts MATCH ?`}
	args = append(args, fuzzyQ)
	if from != "" {
		where = append(where, `datetime(m.created_at) >= datetime(?)`)
		args = append(args, from)
	}
	if to != "" {
		where = append(where, `datetime(m.created_at) <= datetime(?)`)
		args = append(args, to)
	}
	rows, err := DB.Query(`
		SELECT m.id, m.session_id, bm25(messages_fts), `+strictCol+`
		FROM messages_fts
		JOIN messages m ON m.id = messages_fts.rowid
		WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byChat := map[string]*chatCandidate{}
	get := func(session string) *chatCandidate {
		c := byChat[session]
		if c == nil {
			c = &chatCandidate{session: session, updated: metas[session].updated, strictExpr: strictQ, fuzzyExpr: fuzzyQ}
			byChat[session] = c
		}
		return c
	}
	for rows.Next() {
		var session string
		var m messageMatch
		if err := rows.Scan(&m.id, &session, &m.score, &m.strict); err != nil {
			return nil, err
		}
		if _, ok := metas[session]; !ok {
			continue
		}
		c := get(session)
		c.count++
		if m.strict {
			c.strictCount++
		}
		// A match without typos beats any with one; then bm25, lower is better.
		if b := c.best; b == nil || (m.strict && !b.strict) || (m.strict == b.strict && m.score < b.score) {
			mm := m
			c.best = &mm
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for id, meta := range metas {
		if meta.messages == 0 || !meta.inRange {
			continue
		}
		words := searchWords(meta.title)
		// typo: matched allowing for typos, which includes matching exactly.
		exact, typo := !anyPart, !anyPart
		for _, p := range parts {
			e, t := p.inWords(words)
			if anyPart {
				exact, typo = exact || e, typo || e || t
			} else {
				exact, typo = exact && e, typo && (e || t)
			}
		}
		if exact || typo {
			c := get(id)
			c.titleExact, c.titleTypo = exact, typo
		}
	}

	out := make([]*chatCandidate, 0, len(byChat))
	for _, c := range byChat {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.exact() != b.exact() {
			return a.exact()
		}
		if a.titleHit() != b.titleHit() {
			return a.titleHit()
		}
		if (a.best != nil) != (b.best != nil) {
			return a.best != nil
		}
		if a.best != nil && a.best.strict != b.best.strict {
			return a.best.strict
		}
		if a.best != nil && a.best.score != b.best.score {
			return a.best.score < b.best.score
		}
		if a.updated != b.updated {
			return a.updated > b.updated
		}
		return a.session < b.session
	})
	return out, nil
}

// SearchChats runs a Search page query.
func SearchChats(opt ChatSearchOptions) (ChatSearchResult, error) {
	res := ChatSearchResult{
		Chats: []ChatSearchHit{}, Offset: clampOffset(opt.Offset), Match: matchAll,
		Terms: []SearchTerm{}, Variants: []string{},
	}
	parts := parseChatQuery(opt.Query)
	if len(parts) == 0 {
		return res, nil
	}
	if !FTSReady() {
		return res, fmt.Errorf("search index unavailable")
	}
	terms, err := vocabulary()
	if err != nil {
		return res, fmt.Errorf("read search vocabulary: %w", err)
	}
	for i := range parts {
		if !parts[i].phrase {
			parts[i].variants = typoVariants(parts[i].words[0], terms)
		}
	}
	from, to := normalizeDay(opt.From, false), normalizeDay(opt.To, true)
	metas, err := chatMetas(from, to)
	if err != nil {
		return res, fmt.Errorf("read chats: %w", err)
	}
	chats, err := matchChats(parts, false, from, to, metas)
	if err == nil && len(chats) == 0 && len(parts) > 1 {
		res.Match = matchAny
		chats, err = matchChats(parts, true, from, to, metas)
	}
	if err != nil {
		return res, fmt.Errorf("search chats: %w", err)
	}

	seenTerm, seenVariant := map[string]bool{}, map[string]bool{}
	for _, p := range parts {
		for _, w := range p.words {
			if !seenTerm[w] {
				seenTerm[w] = true
				res.Terms = append(res.Terms, SearchTerm{Text: w, Prefix: p.prefix()})
			}
		}
		for _, v := range p.variants {
			if !seenVariant[v] {
				seenVariant[v] = true
				res.Variants = append(res.Variants, v)
			}
		}
	}

	limit := opt.Limit
	if limit <= 0 {
		limit = chatSearchDefaultLimit
	}
	limit = min(limit, chatSearchMaxLimit)
	res.Total = len(chats)
	start := min(res.Offset, len(chats))
	end := min(start+limit, len(chats))
	for _, c := range chats[start:end] {
		meta := metas[c.session]
		hit := ChatSearchHit{
			SessionID: c.session, Title: meta.title, Project: meta.project, Temporary: meta.temporary,
			UpdatedAt: meta.updated, Exact: c.exact(), TitleMatch: c.titleHit(), Matches: c.count,
		}
		if c.strictCount > 0 {
			hit.Matches = c.strictCount
		}
		if c.best != nil {
			expr := c.fuzzyExpr
			if c.best.strict {
				expr = c.strictExpr
			}
			msg, err := bestMessage(c.best.id, expr)
			switch {
			case err == nil:
				hit.Message = &msg
			case errors.Is(err, sql.ErrNoRows):
				// Deleted since the search ran.
				hit.Excerpt = openingMessage(c.session)
			default:
				return res, fmt.Errorf("read match: %w", err)
			}
		} else {
			hit.Excerpt = openingMessage(c.session)
		}
		res.Chats = append(res.Chats, hit)
	}
	return res, nil
}

// bestMessage reads a matching message, with the passage around the match.
func bestMessage(id int64, expr string) (ChatSearchMessage, error) {
	var m ChatSearchMessage
	var author, at sql.NullString
	var snip string
	err := DB.QueryRow(`
		SELECT m.id, m.role, m.author_name, `+rfc3339("m.created_at")+`,
		       snippet(messages_fts, 0, '', '', '…', 24)
		FROM messages_fts
		JOIN messages m ON m.id = messages_fts.rowid
		WHERE messages_fts MATCH ? AND messages_fts.rowid = ?`, expr, id,
	).Scan(&m.ID, &m.Role, &author, &at, &snip)
	m.AuthorName, m.CreatedAt, m.Snippet = author.String, at.String, plainText(snip)
	return m, err
}

// openingMessage is the start of a chat's first message.
func openingMessage(session string) string {
	var content string
	DB.QueryRow(`SELECT content FROM messages WHERE session_id = ? AND role = 'user' ORDER BY id LIMIT 1`, session).Scan(&content)
	return truncate(plainText(content), chatSearchExcerptChars)
}

var (
	mdHeading = regexp.MustCompile(`(?m)^[ \t]*#{1,6}[ \t]+`)
	mdBullet  = regexp.MustCompile(`(?m)^[ \t]*(?:[-*+>]|\d+\.)[ \t]+`)
	mdLink    = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	mdMarks   = strings.NewReplacer("**", "", "__", "", "`", "", "|", " ")
)

// plainText strips the Markdown marks that would read as noise in a snippet,
// and puts it on one line.
func plainText(s string) string {
	s = mdHeading.ReplaceAllString(s, "")
	s = mdBullet.ReplaceAllString(s, "")
	s = mdLink.ReplaceAllString(s, "$1")
	s = mdMarks.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}
