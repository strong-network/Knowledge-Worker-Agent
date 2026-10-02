// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// session is an opencode session whose folder moves.
type session struct {
	ID       string
	Dir      string // its folder before the move
	NewDir   string
	Messages int
	Parts    int
	File     string // its export
}

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// sessionsToRepoint lists the sessions to re-point, and the ids of those under a
// moved folder whose own folder is already gone, which are left as they are.
func sessionsToRepoint(path string, p Plan) ([]session, map[string]bool, error) {
	missing := map[string]bool{}
	db, err := openReadOnly(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, missing, nil
		}
		return nil, nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, directory FROM session ORDER BY id`)
	if err != nil {
		if noSuchTable(err) {
			return nil, missing, nil
		}
		return nil, nil, err
	}
	var out []session
	for rows.Next() {
		var s session
		if err := rows.Scan(&s.ID, &s.Dir); err != nil {
			rows.Close()
			return nil, nil, err
		}
		newDir, moved := p.Remap(s.Dir)
		if !moved {
			continue
		}
		if _, err := os.Stat(s.Dir); err != nil {
			missing[s.ID] = true
			continue
		}
		if !sessionIDRe.MatchString(s.ID) {
			rows.Close()
			return nil, nil, fmt.Errorf("opencode session id %q isn't safe as a file name", s.ID)
		}
		s.NewDir = newDir
		out = append(out, s)
	}
	rows.Close()
	for i := range out {
		if out[i].Messages, err = countRows(db, "message", "session_id", out[i].ID); err != nil {
			return nil, nil, err
		}
		if out[i].Parts, err = countRows(db, "part", "session_id", out[i].ID); err != nil {
			return nil, nil, err
		}
	}
	return out, missing, nil
}

// opencodeExtraEnv is added to opencode's environment; tests use it.
var opencodeExtraEnv []string

// opencodeCommand runs opencode with the user's data but an empty
// configuration (cfgHome), so no connector or plugin starts.
func opencodeCommand(bin, cfgHome, dir string, args ...string) *exec.Cmd {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	home, _ := os.UserHomeDir()
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + cfgHome}, opencodeExtraEnv...)
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		cmd.Env = append(cmd.Env, "XDG_DATA_HOME="+v)
	}
	return cmd
}

func emptyOpencodeConfig(cfgHome string) error {
	dir := filepath.Join(cfgHome, "opencode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(`{"autoupdate":false}`+"\n"), 0o600)
}

// exportSession writes s's export to a file in dir and checks it's complete.
func exportSession(bin, cfgHome, dir string, s *session) error {
	s.File = filepath.Join(dir, s.ID+".json")
	f, err := os.OpenFile(s.File, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	cmd := opencodeCommand(bin, cfgHome, dir, "export", s.ID)
	// A file, never a pipe: through a pipe, opencode's output is cut off at 64 KB.
	cmd.Stdout = f
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	syncErr := f.Sync()
	f.Close()
	if runErr != nil {
		return fmt.Errorf("export of session %s: %v: %s", s.ID, runErr, lastLine(stderr.String()))
	}
	if syncErr != nil {
		return syncErr
	}
	return checkExport(*s)
}

// checkExport checks an export holds the session, and as many messages and
// parts as the database does.
func checkExport(s session) error {
	data, err := os.ReadFile(s.File)
	if err != nil {
		return err
	}
	var e struct {
		Info struct {
			ID string `json:"id"`
		} `json:"info"`
		Messages []struct {
			Parts []json.RawMessage `json:"parts"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return fmt.Errorf("export of session %s isn't complete: %v", s.ID, err)
	}
	if e.Info.ID != s.ID {
		return fmt.Errorf("export of session %s holds session %q", s.ID, e.Info.ID)
	}
	parts := 0
	for _, m := range e.Messages {
		parts += len(m.Parts)
	}
	if len(e.Messages) != s.Messages || parts != s.Parts {
		return fmt.Errorf("export of session %s holds %d messages and %d parts; the database has %d and %d",
			s.ID, len(e.Messages), parts, s.Messages, s.Parts)
	}
	return nil
}

// importSession re-points s: opencode records the folder the import runs from.
func importSession(bin, cfgHome string, s session) error {
	cmd := opencodeCommand(bin, cfgHome, s.NewDir, "import", s.File)
	var outBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outBuf, &outBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("import of session %s: %v: %s", s.ID, err, lastLine(outBuf.String()))
	}
	return nil
}

// verifyRepointed checks every re-pointed session records its new folder with
// the same messages and parts, and no other session records a moved folder,
// except those whose folder was already gone.
func verifyRepointed(path string, p Plan, sessions []session, missing map[string]bool) []string {
	db, err := openReadOnly(path)
	if err != nil {
		return []string{fmt.Sprintf("opencode's database: %v", err)}
	}
	defer db.Close()
	var problems []string
	for _, s := range sessions {
		var dir string
		if err := db.QueryRow(`SELECT directory FROM session WHERE id = ?`, s.ID).Scan(&dir); err != nil {
			problems = append(problems, fmt.Sprintf("session %s: %v", s.ID, err))
			continue
		}
		if dir != s.NewDir {
			problems = append(problems, fmt.Sprintf("session %s records %s, not %s", s.ID, dir, s.NewDir))
		}
		m, err1 := countRows(db, "message", "session_id", s.ID)
		pt, err2 := countRows(db, "part", "session_id", s.ID)
		if err1 != nil || err2 != nil || m != s.Messages || pt != s.Parts {
			problems = append(problems, fmt.Sprintf("session %s has %d messages and %d parts, had %d and %d", s.ID, m, pt, s.Messages, s.Parts))
		}
	}
	rows, err := db.Query(`SELECT id, directory FROM session`)
	if err != nil {
		return append(problems, fmt.Sprintf("opencode's sessions: %v", err))
	}
	defer rows.Close()
	for rows.Next() {
		var id, dir string
		if rows.Scan(&id, &dir) != nil {
			continue
		}
		if _, moved := p.Remap(dir); moved && !missing[id] {
			problems = append(problems, fmt.Sprintf("session %s still records %s", id, dir))
		}
	}
	return problems
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
