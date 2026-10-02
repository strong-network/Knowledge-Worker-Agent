// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"database/sql"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	_ "modernc.org/sqlite"
)

var DB *sql.DB

// dsn builds the SQLite connection string.
//
// The pragma syntax matters: this repo uses modernc.org/sqlite, which accepts
// _pragma=name(value) and *silently ignores* the _journal_mode= / _busy_timeout=
// parameters that mattn/go-sqlite3 uses. Until this was fixed the database ran
// in rollback-journal mode with no busy timeout at all, despite the DSN
// appearing to ask for both. Nothing failed because SetMaxOpenConns(1) leaves a
// single connection that never contends -- but the settings were inert.
//
// WAL lets readers proceed during a write (needed the moment a second process
// or connection reads the database), and busy_timeout makes a lock conflict
// retry for 5s instead of failing instantly with SQLITE_BUSY.
func dsn(path string) string {
	return "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
}

func Init(path string) error {
	var err error
	DB, err = sql.Open("sqlite", dsn(path))
	if err != nil {
		return err
	}
	DB.SetMaxOpenConns(1)
	_, _ = DB.Exec("PRAGMA foreign_keys = ON")
	// Snapshot before migrating: a schema change this build is about to apply is
	// one of the things worth being able to go back from, so the copy has to
	// predate it. Best-effort -- a workspace must still start when the disk is
	// full or read-only.
	if snap, err := Snapshot(path); err != nil {
		log.Printf("[db] snapshot failed (continuing): %v", err)
	} else if snap != "" {
		log.Printf("[db] snapshot written: %s", snap)
	}
	return migrate()
}

// Close closes the database.
//
// No explicit wal_checkpoint is needed: SQLite checkpoints and removes the
// -wal sidecar when the last connection closes, which leaves the .db file
// self-contained and safe to copy. That matters under WAL, where a committed
// row can otherwise live entirely in -wal -- copying just the .db while the
// server is running can yield a database missing recent data, or the table
// itself. TestCloseCheckpointsWAL pins the property.
func Close() {
	if DB != nil {
		DB.Close()
	}
}

func migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		opencode_session_id TEXT DEFAULT '',
		config TEXT DEFAULT '{}',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id);
	CREATE TABLE IF NOT EXISTS model_stats (
		model TEXT PRIMARY KEY,
		requests INTEGER NOT NULL DEFAULT 0,
		premium_reqs INTEGER NOT NULL DEFAULT 0,
		tokens_in INTEGER NOT NULL DEFAULT 0,
		tokens_out INTEGER NOT NULL DEFAULT 0,
		tool_calls INTEGER NOT NULL DEFAULT 0,
		files_modified INTEGER NOT NULL DEFAULT 0,
		lines_added INTEGER NOT NULL DEFAULT 0,
		lines_removed INTEGER NOT NULL DEFAULT 0,
		cost REAL NOT NULL DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS session_stats (
		session_id TEXT PRIMARY KEY,
		requests INTEGER NOT NULL DEFAULT 0,
		tool_calls INTEGER NOT NULL DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS projects (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		workspace_path TEXT NOT NULL DEFAULT '',
		repo_url TEXT NOT NULL DEFAULT '',
		instructions TEXT NOT NULL DEFAULT '',
		data_sources TEXT NOT NULL DEFAULT '[]',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS mcp_status (
		server TEXT PRIMARY KEY,
		authenticated INTEGER NOT NULL DEFAULT 0,
		checked_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS daily_cost (
		day TEXT PRIMARY KEY,
		cost REAL NOT NULL DEFAULT 0,
		requests INTEGER NOT NULL DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS scheduled_tasks (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		prompt TEXT NOT NULL,
		workdir TEXT NOT NULL DEFAULT '',
		repeat TEXT NOT NULL DEFAULT 'none',
		model TEXT NOT NULL DEFAULT '',
		first_run_at TEXT NOT NULL DEFAULT '',
		next_run_at TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		pending_catchup_at TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS task_runs (
		id TEXT PRIMARY KEY,
		task_id TEXT NOT NULL,
		session_id TEXT NOT NULL DEFAULT '',
		trigger TEXT NOT NULL DEFAULT 'scheduled',
		status TEXT NOT NULL DEFAULT 'running',
		error TEXT NOT NULL DEFAULT '',
		opened INTEGER NOT NULL DEFAULT 0,
		scheduled_for TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (task_id) REFERENCES scheduled_tasks(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_task_runs_task ON task_runs(task_id);
	CREATE TABLE IF NOT EXISTS saved_prompts (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		prompt TEXT NOT NULL,
		agent_kind TEXT NOT NULL DEFAULT 'default',
		agent_id TEXT NOT NULL DEFAULT '',
		position INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS app_meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT ''
	);
	-- Chat sharing: the people who have opened a shared chat. The owner is not a row.
	CREATE TABLE IF NOT EXISTS session_guests (
		session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
		guest_id   TEXT NOT NULL,
		name       TEXT NOT NULL,
		first_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_seen  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (session_id, guest_id)
	);
	-- Cross-session retrieval: one generated note per (session, day) of activity.
	--
	-- Deliberately NO FOREIGN KEY: a note outlives the chat it describes.
	-- Deleting a chat is routine hygiene (workspace hygiene encourages clearing chats idle
	-- past 30 days); losing the historical record is not. If notes cascaded,
	-- every cleanup would punch a hole in exactly the old material that
	-- cross-session retrieval exists to reach. Precedent in this same schema:
	-- task_runs.session_id holds a session reference with no foreign key.
	--
	-- The consequence is that a note must be self-describing, hence the
	-- session_title and project_id snapshots: the title does not live in a
	-- column, it lives inside sessions.config, which disappears with the row.
	CREATE TABLE IF NOT EXISTS session_notes (
		session_id     TEXT NOT NULL,
		day            TEXT NOT NULL,            -- YYYY-MM-DD, UTC
		note           TEXT NOT NULL,
		session_title  TEXT NOT NULL DEFAULT '', -- snapshot; sessions row may be gone
		project_id     TEXT NOT NULL DEFAULT '',
		covers_from    INTEGER NOT NULL DEFAULT 0, -- first messages.id covered
		covers_to      INTEGER NOT NULL DEFAULT 0, -- last  messages.id covered
		msg_count      INTEGER NOT NULL DEFAULT 0,
		model          TEXT NOT NULL DEFAULT '',
		prompt_version INTEGER NOT NULL DEFAULT 1,
		created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
		orphaned_at    DATETIME,                 -- set when the session is deleted
		PRIMARY KEY (session_id, day)
	);
	CREATE INDEX IF NOT EXISTS idx_session_notes_day ON session_notes(day);
	`
	if _, err := DB.Exec(schema); err != nil {
		return err
	}
	if err := ensureModelStatsColumns(); err != nil {
		return err
	}
	if err := ensureSessionsColumns(); err != nil {
		return err
	}
	if err := ensureScheduledTaskColumns(); err != nil {
		return err
	}
	if err := ensureMessagesColumns(); err != nil {
		return err
	}
	if err := ensureProjectsColumns(); err != nil {
		return err
	}
	// Cross-session retrieval: the full-text index is derived and disposable, so a failure here
	// must not stop the workspace booting. Chat works without search; search
	// reports itself unavailable rather than returning silently empty results.
	if err := ensureFTS(); err != nil {
		log.Printf("[fts] index unavailable (search will report this): %v", err)
	}
	return nil
}

func CreateSession(id string, cfg config.SessionConfig) error {
	cfg.Backend = config.NormalizeBackend(cfg.Backend)
	cfgJSON, _ := json.Marshal(cfg)
	_, err := DB.Exec(`INSERT INTO sessions (id, config) VALUES (?, ?)`, id, string(cfgJSON))
	return err
}

func GetSessionConfig(id string) (config.SessionConfig, error) {
	var cfgJSON string
	err := DB.QueryRow(`SELECT config FROM sessions WHERE id = ?`, id).Scan(&cfgJSON)
	if err != nil {
		return config.DefaultSessionConfig(), err
	}
	var cfg config.SessionConfig
	json.Unmarshal([]byte(cfgJSON), &cfg)
	cfg.Backend = config.NormalizeBackend(cfg.Backend)
	return cfg, nil
}

func UpdateSessionConfig(id string, cfg config.SessionConfig) error {
	cfg.Backend = config.NormalizeBackend(cfg.Backend)
	cfgJSON, _ := json.Marshal(cfg)
	_, err := DB.Exec(
		`UPDATE sessions SET config = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		string(cfgJSON), id,
	)
	return err
}

func SetOpencodeSession(id, opencodeSessionID string) error {
	_, err := DB.Exec(
		`UPDATE sessions SET opencode_session_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		opencodeSessionID, id,
	)
	return err
}

func GetOpencodeSession(id string) string {
	var sessionID string
	DB.QueryRow(`SELECT opencode_session_id FROM sessions WHERE id = ?`, id).Scan(&sessionID)
	return sessionID
}

func SessionExists(id string) bool {
	var count int
	DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = ?`, id).Scan(&count)
	return count > 0
}

// IsSessionDeprecated reports whether the session is a legacy GitHub Copilot
// session. It inspects the RAW persisted config (bypassing GetSessionConfig,
// which normalizes the backend to "opencode" and would erase the signal).
// Unknown/missing sessions are not deprecated.
func IsSessionDeprecated(id string) bool {
	var cfgJSON string
	if err := DB.QueryRow(`SELECT config FROM sessions WHERE id = ?`, id).Scan(&cfgJSON); err != nil {
		return false
	}
	var cfg config.SessionConfig
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		return false
	}
	return config.IsLegacyCopilotBackend(cfg.Backend)
}

func DeleteSession(id string) error {
	_, err := DB.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

func AddMessage(sessionID, role, content string) error {
	return AddMessageWithUsage(sessionID, role, content, "")
}

// AddMessageWithUsage records a message together with its per-turn usage
// report (a JSON object, or "" when there is none — user messages, or an
// assistant turn that produced no usage event).
func AddMessageWithUsage(sessionID, role, content, usage string) error {
	return AddTurnMessage(sessionID, role, content, usage, "")
}

// AddTurnMessage is AddMessageWithUsage for a message a chat turn produced. The
// turn id lets a viewer match a live turn to the rows it wrote.
func AddTurnMessage(sessionID, role, content, usage, turnID string) error {
	_, err := DB.Exec(
		`INSERT INTO messages (session_id, role, content, usage, turn_id) VALUES (?, ?, ?, ?, ?)`,
		sessionID, role, content, usage, nullable(turnID),
	)
	if err == nil {
		DB.Exec(`UPDATE sessions SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, sessionID)
	}
	return err
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// CreatedAt is RFC3339 UTC. The column is written by SQLite's
	// CURRENT_TIMESTAMP, which is UTC but formatted without a zone, so it is
	// normalised in SQL (see GetMessages) rather than handed to the browser as
	// an ambiguous naive string.
	CreatedAt string `json:"created_at,omitempty"`
	// Usage is the per-turn token/cost report, passed through verbatim.
	// Omitted when the message has none.
	Usage json.RawMessage `json:"usage,omitempty"`
	// AuthorID/AuthorName name a guest who wrote a user message.
	// Empty means the workspace owner.
	AuthorID   string `json:"author_id,omitempty"`
	AuthorName string `json:"author_name,omitempty"`
	// TurnID names the chat turn that wrote the message: the same id is on
	// the turn's frames, so a viewer can tell a live turn it already shows.
	// Empty for messages written before turns had ids.
	TurnID string `json:"turn_id,omitempty"`
}

// CountMessages returns how many messages a session has. It exists so callers
// that only need to know whether a session is on its first turn don't have to
// load the whole transcript.
func CountMessages(sessionID string) int {
	var n int
	if err := DB.QueryRow(
		`SELECT COUNT(*) FROM messages WHERE session_id = ?`, sessionID,
	).Scan(&n); err != nil {
		return 0
	}
	return n
}

func GetMessages(sessionID string) []ChatMessage {
	rows, err := DB.Query(
		`SELECT role, content,
		        strftime('%Y-%m-%dT%H:%M:%SZ', created_at) AS created_at,
		        usage, author_id, author_name, turn_id
		   FROM messages WHERE session_id = ? ORDER BY id ASC`, sessionID,
	)
	if err != nil {
		return []ChatMessage{}
	}
	defer rows.Close()

	var msgs []ChatMessage
	for rows.Next() {
		var m ChatMessage
		// created_at is nullable in the schema, and strftime returns NULL for
		// an unparseable value, so scan through nullable holders.
		var createdAt, usage, authorID, authorName, turnID sql.NullString
		if err := rows.Scan(&m.Role, &m.Content, &createdAt, &usage, &authorID, &authorName, &turnID); err != nil {
			continue
		}
		m.CreatedAt = createdAt.String
		m.AuthorID, m.AuthorName, m.TurnID = authorID.String, authorName.String, turnID.String
		if s := strings.TrimSpace(usage.String); s != "" && json.Valid([]byte(s)) {
			m.Usage = json.RawMessage(s)
		}
		msgs = append(msgs, m)
	}
	if msgs == nil {
		return []ChatMessage{}
	}
	return msgs
}

func UndoLastExchange(sessionID string) (int, int) {
	var total int
	DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, sessionID).Scan(&total)

	removed := 0
	var lastID int
	var lastRole string

	err := DB.QueryRow(
		`SELECT id, role FROM messages WHERE session_id = ? ORDER BY id DESC LIMIT 1`, sessionID,
	).Scan(&lastID, &lastRole)
	if err == nil && lastRole == "assistant" {
		DB.Exec(`DELETE FROM messages WHERE id = ?`, lastID)
		removed++
	}

	err = DB.QueryRow(
		`SELECT id, role FROM messages WHERE session_id = ? ORDER BY id DESC LIMIT 1`, sessionID,
	).Scan(&lastID, &lastRole)
	if err == nil && lastRole == "user" {
		DB.Exec(`DELETE FROM messages WHERE id = ?`, lastID)
		removed++
	}

	if removed > 0 {
		DB.Exec(`UPDATE sessions SET opencode_session_id = '', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, sessionID)
	}
	return removed, total - removed
}

type SessionListItem struct {
	SessionID string `json:"session_id"`
	Messages  int    `json:"messages"`
	Label     string `json:"label"`
	Mode      string `json:"mode"`
	Workdir   string `json:"workdir"`
	Model     string `json:"model"`
	Agent     string `json:"agent"`
	Backend   string `json:"backend"`
	// Deprecated is true for legacy sessions created against the removed
	// GitHub Copilot backend (raw stored config has "backend":"copilot").
	// Such sessions are read-only in the UI and rejected by the chat endpoint.
	Deprecated bool   `json:"deprecated"`
	Group      string `json:"group"`
	Pinned     bool   `json:"pinned"`
	Favorite   bool   `json:"favorite"`
	// ProjectID is the project this chat belongs to ("" for loose chats).
	// ProjectStarred is the project-scoped favorite flag, distinct
	// from the global Favorite flag.
	ProjectID      string `json:"project_id"`
	ProjectStarred bool   `json:"project_starred"`
	Draft          string `json:"draft"`
	// TaskID is set when this chat was produced by a scheduled task run.
	// Such chats are surfaced under the Scheduled tasks section, not
	// in Recent chats. Empty for ordinary chats.
	TaskID string `json:"task_id"`
	// NoteCount is how many day notes describe this chat. It is surfaced
	// so the delete confirmation can say what is at stake rather than asking
	// abstractly -- notes survive deletion by default, and a
	// retention default the user cannot see is a trap. Zero in any workspace
	// that has not enabled day notes.
	NoteCount int `json:"note_count"`
	// Shared and GuestCount are for chat sharing: whether the chat is shared now, and
	// how many people have opened it, for the delete and stop confirmations.
	Shared     bool   `json:"shared"`
	GuestCount int    `json:"guest_count"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

func ListSessions() []SessionListItem {
	rows, err := DB.Query(`
		SELECT s.id, s.config, s.draft, s.project_id, s.project_starred, s.created_at, s.updated_at,
			(SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id) as msg_count,
			(SELECT r.task_id FROM task_runs r WHERE r.session_id = s.id ORDER BY r.created_at ASC LIMIT 1) as task_id,
			(SELECT COUNT(*) FROM session_notes n WHERE n.session_id = s.id) as note_count,
			s.shared_at IS NOT NULL,
			(SELECT COUNT(*) FROM session_guests g WHERE g.session_id = s.id)
		FROM sessions s
		ORDER BY s.updated_at DESC
	`)
	if err != nil {
		return []SessionListItem{}
	}
	defer rows.Close()

	var items []SessionListItem
	for rows.Next() {
		var id, cfgJSON, createdAt, updatedAt string
		var draft, projectID, taskID sql.NullString
		var projectStarred int
		var msgCount int
		var noteCount, guestCount int
		var shared bool
		rows.Scan(&id, &cfgJSON, &draft, &projectID, &projectStarred, &createdAt, &updatedAt, &msgCount, &taskID, &noteCount, &shared, &guestCount)

		var cfg config.SessionConfig
		json.Unmarshal([]byte(cfgJSON), &cfg)
		// Capture the persisted backend BEFORE NormalizeBackend rewrites it to
		// "opencode": a raw value of "copilot" marks a deprecated legacy session.
		deprecated := config.IsLegacyCopilotBackend(cfg.Backend)
		cfg.Backend = config.NormalizeBackend(cfg.Backend)

		items = append(items, SessionListItem{
			SessionID:      id,
			Messages:       msgCount,
			Label:          cfg.Label,
			Mode:           cfg.Mode,
			Workdir:        cfg.Workdir,
			Model:          cfg.Model,
			Agent:          cfg.Agent,
			Backend:        cfg.Backend,
			Deprecated:     deprecated,
			Group:          cfg.Group,
			Pinned:         cfg.Pinned,
			Favorite:       cfg.Favorite,
			ProjectID:      projectID.String,
			ProjectStarred: projectStarred == 1,
			Draft:          draft.String,
			TaskID:         taskID.String,
			NoteCount:      noteCount,
			Shared:         shared,
			GuestCount:     guestCount,
			CreatedAt:      createdAt,
			UpdatedAt:      updatedAt,
		})
	}
	if items == nil {
		return []SessionListItem{}
	}
	return items
}

// scanProjectSessions builds SessionListItems from a rows set selecting
// (id, config, draft, project_starred, created_at, updated_at, msg_count).
// Used by ListProjectSessions.
func scanProjectSessions(rows *sql.Rows) []SessionListItem {
	var items []SessionListItem
	for rows.Next() {
		var id, cfgJSON, createdAt, updatedAt string
		var draft sql.NullString
		var projectStarred, msgCount int
		rows.Scan(&id, &cfgJSON, &draft, &projectStarred, &createdAt, &updatedAt, &msgCount)

		var cfg config.SessionConfig
		json.Unmarshal([]byte(cfgJSON), &cfg)
		deprecated := config.IsLegacyCopilotBackend(cfg.Backend)
		cfg.Backend = config.NormalizeBackend(cfg.Backend)

		items = append(items, SessionListItem{
			SessionID:      id,
			Messages:       msgCount,
			Label:          cfg.Label,
			Mode:           cfg.Mode,
			Workdir:        cfg.Workdir,
			Model:          cfg.Model,
			Agent:          cfg.Agent,
			Backend:        cfg.Backend,
			Deprecated:     deprecated,
			Group:          cfg.Group,
			Pinned:         cfg.Pinned,
			Favorite:       cfg.Favorite,
			ProjectStarred: projectStarred == 1,
			Draft:          draft.String,
			CreatedAt:      createdAt,
			UpdatedAt:      updatedAt,
		})
	}
	if items == nil {
		return []SessionListItem{}
	}
	return items
}

type ModelStat struct {
	Model         string  `json:"model"`
	Requests      int     `json:"requests"`
	Count         int     `json:"count"`
	PremiumReqs   int     `json:"premium_reqs"`
	TokensIn      int     `json:"tokens_in"`
	TokensOut     int     `json:"tokens_out"`
	TotalInput    int     `json:"total_input"`
	TotalOutput   int     `json:"total_output"`
	ToolCalls     int     `json:"tool_calls"`
	FilesModified int     `json:"files_modified"`
	LinesAdded    int     `json:"lines_added"`
	LinesRemoved  int     `json:"lines_removed"`
	Cost          float64 `json:"cost"`
	UpdatedAt     string  `json:"updated_at"`
}

type UsageSummary struct {
	TotalSessions                int     `json:"total_sessions"`
	ActiveSessions               int     `json:"active_sessions"`
	TotalRequests                int     `json:"total_requests"`
	TotalToolCalls               int     `json:"total_tool_calls"`
	AvgToolCallsPerSession       float64 `json:"avg_tool_calls_per_session"`
	AvgToolCallsPerActiveSession float64 `json:"avg_tool_calls_per_active_session"`
	AvgToolCallsPerRequest       float64 `json:"avg_tool_calls_per_request"`
}

// DailyCost is one day's rolled-up request cost, for the stats window's
// per-day breakdown. Day is "YYYY-MM-DD" (server local time).
type DailyCost struct {
	Day      string  `json:"day"`
	Cost     float64 `json:"cost"`
	Requests int     `json:"requests"`
}

func IncrementModelStats(model string, premiumReqs, tokensIn, tokensOut, toolCalls, filesModified, linesAdded, linesRemoved int, cost float64) error {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "default"
	}
	_, err := DB.Exec(`
		INSERT INTO model_stats (
			model, requests, premium_reqs, tokens_in, tokens_out,
			tool_calls, files_modified, lines_added, lines_removed, cost, updated_at
		)
		VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(model) DO UPDATE SET
			requests = requests + 1,
			premium_reqs = premium_reqs + excluded.premium_reqs,
			tokens_in = tokens_in + excluded.tokens_in,
			tokens_out = tokens_out + excluded.tokens_out,
			tool_calls = tool_calls + excluded.tool_calls,
			files_modified = files_modified + excluded.files_modified,
			lines_added = lines_added + excluded.lines_added,
			lines_removed = lines_removed + excluded.lines_removed,
			cost = cost + excluded.cost,
			updated_at = CURRENT_TIMESTAMP
	`, model, premiumReqs, tokensIn, tokensOut, toolCalls, filesModified, linesAdded, linesRemoved, cost)
	if err != nil {
		return err
	}
	// Also roll the cost up into today's bucket so the stats window can show a
	// per-day cost breakdown. Best-effort: a daily-cost failure must not mask a
	// successful model_stats write, but we surface it if the main write is fine.
	return recordDailyCost(cost)
}

// recordDailyCost adds cost (and one request) to the current local day's bucket
// in daily_cost. The day key is YYYY-MM-DD in the server's local time zone, so
// "today" matches the operator's wall clock. Rows accrue only from when this
// ships — historical per-day cost cannot be reconstructed from the lifetime
// model_stats totals.
func recordDailyCost(cost float64) error {
	day := time.Now().Format("2006-01-02")
	_, err := DB.Exec(`
		INSERT INTO daily_cost (day, cost, requests, updated_at)
		VALUES (?, ?, 1, CURRENT_TIMESTAMP)
		ON CONFLICT(day) DO UPDATE SET
			cost = cost + excluded.cost,
			requests = requests + 1,
			updated_at = CURRENT_TIMESTAMP
	`, day, cost)
	return err
}

func IncrementSessionStats(sessionID string, toolCalls int) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	_, err := DB.Exec(`
		INSERT INTO session_stats (session_id, requests, tool_calls, updated_at)
		VALUES (?, 1, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(session_id) DO UPDATE SET
			requests = requests + 1,
			tool_calls = tool_calls + excluded.tool_calls,
			updated_at = CURRENT_TIMESTAMP
	`, sessionID, toolCalls)
	return err
}

func GetModelStats() []ModelStat {
	rows, err := DB.Query(`
		SELECT model, requests, premium_reqs, tokens_in, tokens_out, tool_calls,
			files_modified, lines_added, lines_removed, cost, updated_at
		FROM model_stats
		ORDER BY requests DESC, model ASC
	`)
	if err != nil {
		return []ModelStat{}
	}
	defer rows.Close()

	var stats []ModelStat
	for rows.Next() {
		var stat ModelStat
		rows.Scan(
			&stat.Model, &stat.Requests, &stat.PremiumReqs, &stat.TokensIn, &stat.TokensOut, &stat.ToolCalls,
			&stat.FilesModified, &stat.LinesAdded, &stat.LinesRemoved, &stat.Cost, &stat.UpdatedAt,
		)
		stat.Count = stat.Requests
		stat.TotalInput = stat.TokensIn
		stat.TotalOutput = stat.TokensOut
		stats = append(stats, stat)
	}
	if stats == nil {
		return []ModelStat{}
	}
	return stats
}

func GetUsageSummary() UsageSummary {
	var summary UsageSummary
	DB.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&summary.TotalSessions)
	DB.QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(SUM(requests), 0),
			COALESCE(SUM(tool_calls), 0)
		FROM session_stats
		WHERE requests > 0
	`).Scan(&summary.ActiveSessions, &summary.TotalRequests, &summary.TotalToolCalls)

	if summary.TotalSessions > 0 {
		summary.AvgToolCallsPerSession = float64(summary.TotalToolCalls) / float64(summary.TotalSessions)
	}
	if summary.ActiveSessions > 0 {
		summary.AvgToolCallsPerActiveSession = float64(summary.TotalToolCalls) / float64(summary.ActiveSessions)
	}
	if summary.TotalRequests > 0 {
		summary.AvgToolCallsPerRequest = float64(summary.TotalToolCalls) / float64(summary.TotalRequests)
	}
	return summary
}

// GetDailyCost returns the request cost for each of the last `days` calendar
// days (server local time), oldest first and zero-filled: every day in the
// window is present even if no requests ran that day. `days` is clamped to
// [1, 90]. The list is always non-nil.
func GetDailyCost(days int) []DailyCost {
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}

	// Pull stored rows within the window into a map for O(1) lookup.
	stored := map[string]DailyCost{}
	rows, err := DB.Query(`
		SELECT day, cost, requests
		FROM daily_cost
		WHERE day >= ?
		ORDER BY day ASC
	`, time.Now().AddDate(0, 0, -(days-1)).Format("2006-01-02"))
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var d DailyCost
			if err := rows.Scan(&d.Day, &d.Cost, &d.Requests); err == nil {
				stored[d.Day] = d
			}
		}
	}

	// Emit exactly `days` entries, oldest → newest, filling gaps with zeros.
	out := make([]DailyCost, 0, days)
	for i := days - 1; i >= 0; i-- {
		day := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		if d, ok := stored[day]; ok {
			out = append(out, d)
		} else {
			out = append(out, DailyCost{Day: day})
		}
	}
	return out
}

// ensureMessagesColumns is the idempotent migration for the messages table.
func ensureMessagesColumns() error {
	return ensureColumns("messages", map[string]string{
		// usage is the per-turn token/cost report for an assistant message,
		// stored as the same JSON object the UI receives over SSE. Kept as one
		// blob rather than a column per metric because nothing queries the
		// individual numbers — aggregate reporting is served by model_stats.
		// Empty for user messages and for assistant turns recorded before this
		// column existed; the UI simply shows no footer for those.
		"usage": "ALTER TABLE messages ADD COLUMN usage TEXT NOT NULL DEFAULT ''",
		// Chat sharing: who wrote a user message. NULL is the workspace owner, which keeps
		// every row written before sharing existed correct without a backfill.
		"author_id":   "ALTER TABLE messages ADD COLUMN author_id TEXT",
		"author_name": "ALTER TABLE messages ADD COLUMN author_name TEXT",
		// Chat sharing: the chat turn a message belongs to, matching turn_id on the
		// turn's frames. NULL for rows written before turns had ids.
		"turn_id": "ALTER TABLE messages ADD COLUMN turn_id TEXT",
	})
}

// ensureModelStatsColumns is the idempotent migration for the model_stats
// table.
func ensureModelStatsColumns() error {
	return ensureColumns("model_stats", map[string]string{
		"tool_calls":     "ALTER TABLE model_stats ADD COLUMN tool_calls INTEGER NOT NULL DEFAULT 0",
		"files_modified": "ALTER TABLE model_stats ADD COLUMN files_modified INTEGER NOT NULL DEFAULT 0",
		"lines_added":    "ALTER TABLE model_stats ADD COLUMN lines_added INTEGER NOT NULL DEFAULT 0",
		"lines_removed":  "ALTER TABLE model_stats ADD COLUMN lines_removed INTEGER NOT NULL DEFAULT 0",
		"cost":           "ALTER TABLE model_stats ADD COLUMN cost REAL NOT NULL DEFAULT 0",
	})
}

// ensureSessionsColumns is the idempotent migration for the sessions table.
// New columns added here keep working when the database file was created by
// an older build.
func ensureSessionsColumns() error {
	return ensureColumns("sessions", map[string]string{
		"draft":               "ALTER TABLE sessions ADD COLUMN draft TEXT NOT NULL DEFAULT ''",
		"opencode_session_id": "ALTER TABLE sessions ADD COLUMN opencode_session_id TEXT DEFAULT ''",
		"project_id":          "ALTER TABLE sessions ADD COLUMN project_id TEXT NOT NULL DEFAULT ''",
		"project_starred":     "ALTER TABLE sessions ADD COLUMN project_starred INTEGER NOT NULL DEFAULT 0",
		// Durable-storage backup/retention state. Kept as real columns
		// (not inside the config JSON blob) so tidy-up eligibility can be
		// computed with SQL. See internal/db/backup.go.
		"backup_remote":  "ALTER TABLE sessions ADD COLUMN backup_remote TEXT NOT NULL DEFAULT ''",
		"last_synced_at": "ALTER TABLE sessions ADD COLUMN last_synced_at DATETIME",
		"backup_status":  "ALTER TABLE sessions ADD COLUMN backup_status TEXT NOT NULL DEFAULT ''",
		// Workspace hygiene: when the user chose "Keep for now" in the review surface, the
		// chat is hidden from it until this timestamp. Deliberately separate
		// from the config Keep flag, which is permanent: a snooze defers the
		// question, it does not answer it forever.
		"tidy_snooze_until": "ALTER TABLE sessions ADD COLUMN tidy_snooze_until TEXT NOT NULL DEFAULT ''",
		// Chat sharing: NULL means not shared; the only source of truth for sharing.
		"shared_at": "ALTER TABLE sessions ADD COLUMN shared_at DATETIME",
		// What guests may do in a shared chat, off by default.
		"share_allow_permissions": "ALTER TABLE sessions ADD COLUMN share_allow_permissions INTEGER NOT NULL DEFAULT 0",
		"share_allow_files":       "ALTER TABLE sessions ADD COLUMN share_allow_files INTEGER NOT NULL DEFAULT 0",
	})
}

// ensureProjectsColumns is the idempotent migration for the projects table.
// The base projects table is created in migrate()'s schema string
// without these columns, so older databases need them added here.
func ensureProjectsColumns() error {
	return ensureColumns("projects", map[string]string{
		// keep marks a project exempt from inactivity cleanup, retained
		// indefinitely.
		"keep": "ALTER TABLE projects ADD COLUMN keep INTEGER NOT NULL DEFAULT 0",
		// last_synced_at records the last successful durable-store sync.
		"last_synced_at": "ALTER TABLE projects ADD COLUMN last_synced_at DATETIME",
		// backup_status is the last known sync state (see backup.go).
		"backup_status": "ALTER TABLE projects ADD COLUMN backup_status TEXT NOT NULL DEFAULT ''",
		// mcp_selection is the project-level MCP connector selection,
		// a JSON name→bool map shared by the project's chats.
		"mcp_selection": "ALTER TABLE projects ADD COLUMN mcp_selection TEXT NOT NULL DEFAULT '{}'",
	})
}

// ensureScheduledTaskColumns is the idempotent migration for the
// scheduled_tasks table. Databases created before per-task model
// selection need the model column added.
func ensureScheduledTaskColumns() error {
	return ensureColumns("scheduled_tasks", map[string]string{
		"model": "ALTER TABLE scheduled_tasks ADD COLUMN model TEXT NOT NULL DEFAULT ''",
	})
}

// ensureColumns adds any missing columns to a table using SQLite's
// PRAGMA table_info to discover the existing set. Each map value is a full
// "ALTER TABLE … ADD COLUMN …" statement (hardcoded constants, never built
// from user input). Idempotent: existing columns are skipped.
func ensureColumns(table string, columns map[string]string) error {
	// #nosec G202 — table is a hardcoded caller constant, not user input.
	rows, err := DB.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for name, statement := range columns {
		if existing[name] {
			continue
		}
		if _, err := DB.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

// GetSessionDraft returns the in-progress, unsent prompt text saved for the
// session, or "" if none. Missing sessions return "" without error.
func GetSessionDraft(id string) string {
	var draft sql.NullString
	if err := DB.QueryRow(`SELECT draft FROM sessions WHERE id = ?`, id).Scan(&draft); err != nil {
		return ""
	}
	return draft.String
}

// SetSessionDraft persists the in-progress prompt text for the session. An
// empty string clears the draft. We intentionally do NOT bump updated_at:
// drafts should not promote a session to the top of the sidebar.
func SetSessionDraft(id, draft string) error {
	_, err := DB.Exec(`UPDATE sessions SET draft = ? WHERE id = ?`, draft, id)
	return err
}

// ── MCP auth status cache ──
//
// Checking whether a remote MCP server is authenticated requires running
// `opencode mcp list`, which connects to each server and can take several
// seconds. We cache the last-known result per server so the UI can render the
// sign-in state instantly, while a background job re-checks and updates it.

// SetMcpStatus upserts the cached authenticated state for an MCP server.
func SetMcpStatus(server string, authenticated bool) {
	if DB == nil {
		return
	}
	auth := 0
	if authenticated {
		auth = 1
	}
	_, _ = DB.Exec(`
		INSERT INTO mcp_status (server, authenticated, checked_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(server) DO UPDATE SET
			authenticated = excluded.authenticated,
			checked_at = excluded.checked_at
	`, server, auth)
}

// GetMcpStatus returns the cached authenticated state for a server and whether
// a cached value exists at all.
func GetMcpStatus(server string) (authenticated bool, known bool) {
	if DB == nil {
		return false, false
	}
	var auth int
	err := DB.QueryRow(`SELECT authenticated FROM mcp_status WHERE server = ?`, server).Scan(&auth)
	if err != nil {
		return false, false
	}
	return auth == 1, true
}

// AllMcpStatus returns the cached authenticated state for every known server.
func AllMcpStatus() map[string]bool {
	out := map[string]bool{}
	if DB == nil {
		return out
	}
	rows, err := DB.Query(`SELECT server, authenticated FROM mcp_status`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var server string
		var auth int
		if err := rows.Scan(&server, &auth); err == nil {
			out[server] = auth == 1
		}
	}
	return out
}
