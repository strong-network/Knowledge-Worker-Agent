// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package guest

import (
	"archive/zip"
	"bytes"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// filesEnv is a shared chat whose folder holds a few files, beside a secret
// outside it that nothing a guest sends may reach.
type filesEnv struct {
	*guestEnv
	sid, dir, secret string
}

func newFilesEnv(t *testing.T, allow bool) *filesEnv {
	t.Helper()
	e := newEnv(t)
	base := t.TempDir()
	t.Setenv("HOME", filepath.Join(base, "home"))
	dir := filepath.Join(base, "home", "Chats", "chat-1")
	for _, d := range []string{"docs", ".git", ".opencode/agent"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	write := func(p, s string) { os.WriteFile(p, []byte(s), 0o644) }
	write(filepath.Join(dir, "notes.md"), "# Notes")
	write(filepath.Join(dir, "docs", "plan.txt"), "the plan")
	write(filepath.Join(dir, ".env"), "TOKEN="+secret)
	write(filepath.Join(dir, ".git", "config"), secret)
	secretFile := filepath.Join(base, "home", ".ssh-key")
	write(secretFile, secret)
	os.Symlink(secretFile, filepath.Join(dir, "key-link"))
	os.Symlink(filepath.Join(base, "home"), filepath.Join(dir, "home-link"))

	sid := chat.NewUUID()
	if err := db.CreateSession(sid, config.SessionConfig{Label: "Files", Workdir: dir}); err != nil {
		t.Fatal(err)
	}
	db.SetSessionShared(sid, true)
	if allow {
		db.SetShareOptions(sid, db.ShareOptions{AllowFiles: true})
	}
	e.identify("Sarah")
	return &filesEnv{guestEnv: e, sid: sid, dir: dir, secret: secretFile}
}

func (f *filesEnv) url(verb, path string) string {
	return "/guest/api/sessions/" + f.sid + "/files/" + verb + "?path=" + path
}

func TestFilesAreOffUntilTheOwnerTurnsThemOn(t *testing.T) {
	f := newFilesEnv(t, false)
	if code, _, _ := f.do("GET", f.url("browse", ""), "", nil); code != 403 {
		t.Errorf("browse with files off = %d, want 403", code)
	}
	db.SetShareOptions(f.sid, db.ShareOptions{AllowFiles: true})
	if code, body, _ := f.do("GET", f.url("browse", ""), "", nil); code != 200 || !strings.Contains(body, "notes.md") {
		t.Errorf("browse with files on = %d %s", code, body)
	}
	db.SetSessionShared(f.sid, false)
	db.SetSessionShared(f.sid, true)
	if code, _, _ := f.do("GET", f.url("browse", ""), "", nil); code != 403 {
		t.Errorf("files stayed on across a new share: %d", code)
	}
}

// Nothing a guest can send reaches a byte outside the chat's folder, or a
// hidden path inside it.
func TestFilesStayInsideTheChatFolder(t *testing.T) {
	f := newFilesEnv(t, true)
	for _, p := range []string{
		"../.ssh-key", "..", "/etc/passwd", f.secret, "key-link", "home-link/.ssh-key",
		".env", ".git/config", "docs/../../.ssh-key", ".opencode/agent", "docs/./../.env",
	} {
		for _, verb := range []string{"browse", "view", "raw", "download"} {
			code, body, _ := f.do("GET", f.url(verb, p), "", nil)
			if code == 200 || strings.Contains(body, secret) {
				t.Errorf("GET %s %q = %d %.80s", verb, p, code, body)
			}
		}
	}
	code, body, _ := f.do("GET", f.url("browse", ""), "", nil)
	if code != 200 {
		t.Fatalf("browse: %d", code)
	}
	for _, name := range []string{".env", ".git", ".opencode", "key-link", "home-link"} {
		if strings.Contains(body, `"`+name+`"`) {
			t.Errorf("browse lists %s: %s", name, body)
		}
	}
	if !strings.Contains(body, `"path":"docs"`) || !strings.Contains(body, `"path":"notes.md"`) || strings.Contains(body, f.dir) {
		t.Errorf("browse paths are not folder-relative: %s", body)
	}
	if code, body, _ := f.do("GET", f.url("view", "docs/plan.txt"), "", nil); code != 200 || !strings.Contains(body, `"content":"the plan"`) || !strings.Contains(body, `"path":"docs/plan.txt"`) {
		t.Errorf("view inside the folder: %d %s", code, body)
	}

	code, body, _ = f.do("GET", f.url("download", ""), "", nil)
	if code != 200 {
		t.Fatalf("zip: %d", code)
	}
	zr, err := zip.NewReader(strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, zf := range zr.File {
		names = append(names, zf.Name)
		rc, _ := zf.Open()
		var b bytes.Buffer
		b.ReadFrom(rc)
		rc.Close()
		if strings.Contains(b.String(), secret) {
			t.Errorf("zip entry %s carries the secret", zf.Name)
		}
	}
	joined := strings.Join(names, " ")
	if !strings.Contains(joined, "files/docs/plan.txt") || strings.Contains(joined, ".env") || strings.Contains(joined, "link") {
		t.Errorf("zip holds %v", names)
	}
}

// A jail that contains the credentials is not a jail.
func TestTheHomeFolderIsNeverShared(t *testing.T) {
	f := newFilesEnv(t, true)
	home := os.Getenv("HOME")
	link := filepath.Join(t.TempDir(), "looks-harmless")
	os.Symlink(home, link)
	for _, dir := range []string{home, filepath.Dir(home), "/", link} {
		if _, err := FilesFolder(dir); err != errFolderIsHome {
			t.Errorf("FilesFolder(%s) = %v, want the home refusal", dir, err)
		}
	}
	if _, err := FilesFolder(f.dir); err != nil {
		t.Errorf("the chat's own folder was refused: %v", err)
	}
	// Checked on every request, not only when the toggle is set.
	cfg, _ := db.GetSessionConfig(f.sid)
	cfg.Workdir = home
	db.UpdateSessionConfig(f.sid, cfg)
	if code, body, _ := f.do("GET", f.url("browse", ""), "", nil); code != 403 || strings.Contains(body, ".ssh-key") {
		t.Errorf("browse of a chat now pointed at home = %d %s", code, body)
	}
}

func upload(t *testing.T, f *filesEnv, target string, names ...string) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("target_dir", target)
	for _, n := range names {
		w, _ := mw.CreateFormFile("files", n)
		w.Write([]byte("uploaded " + n))
	}
	mw.Close()
	code, body, _ := f.do("POST", "/guest/api/sessions/"+f.sid+"/files/upload", buf.String(), map[string]string{"Origin": testOrigin, "Content-Type": mw.FormDataContentType()})
	return code, body
}

// Guests read and add; they never delete, rename or move, and an upload never
// replaces what is there.
func TestGuestWritesAreAdditive(t *testing.T) {
	f := newFilesEnv(t, true)
	post := func(verb, body string, hdr map[string]string) (int, string) {
		code, b, _ := f.do("POST", "/guest/api/sessions/"+f.sid+"/files/"+verb, body, hdr)
		return code, b
	}
	if code, _ := post("save", `{"path":"notes.md","content":"x"}`, map[string]string{"Content-Type": "application/json"}); code != 403 {
		t.Errorf("save without the share origin = %d", code)
	}
	if code, body := post("save", `{"path":"notes.md","content":"# Edited"}`, sameOrigin); code != 200 {
		t.Errorf("save = %d %s", code, body)
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, "notes.md")); string(b) != "# Edited" {
		t.Errorf("saved content = %q", b)
	}
	for _, p := range []string{"../escape.txt", ".opencode/agent/evil.md", "docs", "missing/new.txt", f.dir + "/abs.txt"} {
		if code, _ := post("save", `{"path":"`+p+`","content":"x"}`, sameOrigin); code == 200 {
			t.Errorf("save %q accepted", p)
		}
	}
	if _, err := os.Stat(filepath.Join(f.dir, ".opencode", "agent", "evil.md")); err == nil {
		t.Error("a guest wrote an agent definition")
	}

	if code, body := post("directory", `{"path":"out"}`, sameOrigin); code != 200 || !strings.Contains(body, `"path":"out"`) {
		t.Errorf("mkdir = %d %s", code, body)
	}
	if code, _ := post("directory", `{"path":"out"}`, sameOrigin); code != 409 {
		t.Errorf("mkdir of an existing folder = %d", code)
	}

	code, body := upload(t, f, "", "notes.md", ".bashrc", "../../up.txt")
	if code != 200 || !strings.Contains(body, `"path":"notes_1.md"`) || !strings.Contains(body, `"path":"upload_1"`) || !strings.Contains(body, `"path":"up.txt"`) {
		t.Errorf("upload = %d %s", code, body)
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, "notes.md")); string(b) != "# Edited" {
		t.Error("an upload replaced an existing file")
	}
	if code, body := upload(t, f, "upload", "a.png"); code != 200 || !strings.Contains(body, `"path":"upload/a.png"`) {
		t.Errorf("upload into a new folder = %d %s", code, body)
	}
	if code, _ := upload(t, f, "../x", "a.png"); code != 400 {
		t.Errorf("upload outside the folder = %d", code)
	}

	for _, verb := range []string{"delete", "rename", "move", "duplicate"} {
		if code, _ := post(verb, `{"path":"notes.md","src":"notes.md","dest":"gone.md"}`, sameOrigin); code != 404 && code != 405 {
			t.Errorf("POST %s = %d, want it absent", verb, code)
		}
	}
	if _, err := os.Stat(filepath.Join(f.dir, "notes.md")); err != nil {
		t.Error("notes.md is gone")
	}
}

// The durable layout's reserved names stay reserved at a share root.
func TestGuestWritesRespectReservedNames(t *testing.T) {
	f := newFilesEnv(t, true)
	os.MkdirAll(filepath.Join(f.dir, ".system"), 0o755)
	if code, _, _ := f.do("POST", "/guest/api/sessions/"+f.sid+"/files/directory", `{"path":"inputs"}`, sameOrigin); code != 409 {
		t.Errorf("mkdir inputs at a share root = %d, want 409", code)
	}
	if code, body := upload(t, f, "", "working"); code != 200 || strings.Contains(body, `"path":"working"`) {
		t.Errorf("an upload took a reserved name: %d %s", code, body)
	}
}

// Previews are framed by the guest page only, and nothing but a PDF runs
// unsandboxed on the origin that holds the guest's cookie.
func TestRawPreviewsAreSandboxed(t *testing.T) {
	f := newFilesEnv(t, true)
	os.WriteFile(filepath.Join(f.dir, "page.html"), []byte("<script>fetch('/guest/api/chat')</script>"), 0o644)
	os.WriteFile(filepath.Join(f.dir, "pic.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), 0o644)
	os.WriteFile(filepath.Join(f.dir, "doc.pdf"), []byte("%PDF-1.4"), 0o644)
	for name, wantSandbox := range map[string]bool{"page.html": true, "pic.svg": true, "notes.md": true, "doc.pdf": false} {
		code, _, h := f.do("GET", f.url("raw", name), "", nil)
		csp := h.Get("Content-Security-Policy")
		if code != 200 || h.Get("X-Frame-Options") != "SAMEORIGIN" || !strings.Contains(csp, "frame-ancestors 'self'") {
			t.Errorf("%s: %d frame=%q csp=%q", name, code, h.Get("X-Frame-Options"), csp)
		}
		if strings.Contains(csp, "sandbox") != wantSandbox {
			t.Errorf("%s: sandboxed=%v, want %v (csp %q)", name, !wantSandbox, wantSandbox, csp)
		}
	}
	if _, _, h := f.do("GET", f.url("download", "page.html"), "", nil); h.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(h.Get("Content-Disposition"), "attachment") {
		t.Errorf("download is not an attachment: %v", h)
	}
	if _, _, h := f.do("GET", "/guest/api/sessions/"+f.sid, "", nil); h.Get("X-Frame-Options") != "DENY" {
		t.Error("framing is now allowed beyond previews")
	}
}

func TestViewDescribesImagesAndBinaries(t *testing.T) {
	f := newFilesEnv(t, true)
	os.WriteFile(filepath.Join(f.dir, "a.png"), []byte{0x89, 'P', 'N', 'G'}, 0o644)
	os.WriteFile(filepath.Join(f.dir, "b.bin"), []byte{1, 0, 2}, 0o644)
	if _, body, _ := f.do("GET", f.url("view", "a.png"), "", nil); !strings.Contains(body, `"type":"image"`) || !strings.Contains(body, `"mime":"image/png"`) {
		t.Errorf("png: %s", body)
	}
	if _, body, _ := f.do("GET", f.url("view", "b.bin"), "", nil); !strings.Contains(body, `"type":"binary"`) {
		t.Errorf("binary: %s", body)
	}
}
