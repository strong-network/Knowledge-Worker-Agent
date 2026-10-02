// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package vertexauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestProjectLocationDefaults(t *testing.T) {
	for _, k := range []string{"GOOGLE_VERTEX_PROJECT", "ANTHROPIC_VERTEX_PROJECT_ID", "GOOGLE_VERTEX_LOCATION", "CLOUD_ML_REGION"} {
		t.Setenv(k, "")
	}
	if got := Project(); got != "" {
		t.Errorf("Project with nothing set = %q, want empty: there is no built-in project", got)
	}
	if got := Location(); got != defaultLocation {
		t.Errorf("Location default = %q, want %q", got, defaultLocation)
	}
}

func TestProjectLocationPrecedence(t *testing.T) {
	// Claude-Code-style vars are honored when the GOOGLE_VERTEX_* ones are unset.
	t.Setenv("GOOGLE_VERTEX_PROJECT", "")
	t.Setenv("GOOGLE_VERTEX_LOCATION", "")
	t.Setenv("ANTHROPIC_VERTEX_PROJECT_ID", "my-proj")
	t.Setenv("CLOUD_ML_REGION", "us-east5")
	if got := Project(); got != "my-proj" {
		t.Errorf("Project = %q, want my-proj", got)
	}
	if got := Location(); got != "us-east5" {
		t.Errorf("Location = %q, want us-east5", got)
	}
	// GOOGLE_VERTEX_* win when both are present.
	t.Setenv("GOOGLE_VERTEX_PROJECT", "gv-proj")
	t.Setenv("GOOGLE_VERTEX_LOCATION", "global")
	if got := Project(); got != "gv-proj" {
		t.Errorf("Project = %q, want gv-proj", got)
	}
	if got := Location(); got != "global" {
		t.Errorf("Location = %q, want global", got)
	}
}

func TestEnsureEnv(t *testing.T) {
	t.Setenv("GOOGLE_VERTEX_PROJECT", "")
	t.Setenv("GOOGLE_VERTEX_LOCATION", "")
	t.Setenv("ANTHROPIC_VERTEX_PROJECT_ID", "seed-proj")
	t.Setenv("CLOUD_ML_REGION", "seed-region")
	proj, loc := EnsureEnv()
	if proj != "seed-proj" || loc != "seed-region" {
		t.Fatalf("EnsureEnv returned (%q,%q)", proj, loc)
	}
	if os.Getenv("GOOGLE_VERTEX_PROJECT") != "seed-proj" {
		t.Errorf("GOOGLE_VERTEX_PROJECT not seeded: %q", os.Getenv("GOOGLE_VERTEX_PROJECT"))
	}
	if os.Getenv("GOOGLE_VERTEX_LOCATION") != "seed-region" {
		t.Errorf("GOOGLE_VERTEX_LOCATION not seeded: %q", os.Getenv("GOOGLE_VERTEX_LOCATION"))
	}
}

func TestEnsureEnvLeavesAMissingProjectUnset(t *testing.T) {
	for _, k := range []string{"GOOGLE_VERTEX_PROJECT", "ANTHROPIC_VERTEX_PROJECT_ID"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	if proj, _ := EnsureEnv(); proj != "" {
		t.Fatalf("EnsureEnv project = %q, want empty", proj)
	}
	if _, set := os.LookupEnv("GOOGLE_VERTEX_PROJECT"); set {
		t.Errorf("GOOGLE_VERTEX_PROJECT was set with no project to set it to")
	}
}

// Without a credentials file the answer is "not signed in", and gcloud is never
// asked: even one that would hand out a token isn't run.
func TestCheckStatus_WithoutADCFileDoesNotRunGcloud(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	isolateADC(t)
	ran := filepath.Join(t.TempDir(), "ran")
	bin := writeFakeGcloud(t, "#!/bin/sh\ntouch "+ran+"\necho ya29.fake-token\n")
	restore := swapBin(bin)
	defer restore()

	if CheckStatus().Authenticated {
		t.Fatal("expected not authenticated without an ADC file")
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("gcloud was run")
	}
}

// TestCheckStatus_ADCFileOnDisk proves the fast path: when the ADC file exists,
// CheckStatus reports authenticated without ever invoking gcloud (the stub here
// always exits 1).
func TestCheckStatus_ADCFileOnDisk(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	dir := t.TempDir()
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", dir)
	if err := os.WriteFile(filepath.Join(dir, "application_default_credentials.json"),
		[]byte(`{"type":"authorized_user"}`), 0o600); err != nil {
		t.Fatalf("write adc: %v", err)
	}
	bin := writeFakeGcloud(t, "#!/bin/sh\nexit 1\n")
	restore := swapBin(bin)
	defer restore()

	st := CheckStatus()
	if !st.Authenticated {
		t.Fatal("expected authenticated from on-disk ADC file without calling gcloud")
	}
	if st.Provider != Provider {
		t.Errorf("provider = %q, want %q", st.Provider, Provider)
	}

	rec := httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/vertex/auth/status", nil))
	var got Status
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || !got.Authenticated {
		t.Errorf("handler: code %d, %+v, %v; want 200 and authenticated", rec.Code, got, err)
	}
}

// TestCheckStatus_GoogleAppCredsPrecedence checks that
// GOOGLE_APPLICATION_CREDENTIALS wins over CLOUDSDK_CONFIG when resolving the
// ADC file path.
func TestCheckStatus_GoogleAppCredsPrecedence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	dir := t.TempDir()
	creds := filepath.Join(dir, "explicit-adc.json")
	if err := os.WriteFile(creds, []byte(`{"type":"authorized_user"}`), 0o600); err != nil {
		t.Fatalf("write adc: %v", err)
	}
	t.Setenv("CLOUDSDK_CONFIG", t.TempDir()) // empty dir, no ADC file
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", creds)
	bin := writeFakeGcloud(t, "#!/bin/sh\nexit 1\n")
	restore := swapBin(bin)
	defer restore()

	if !CheckStatus().Authenticated {
		t.Fatal("expected authenticated from GOOGLE_APPLICATION_CREDENTIALS file")
	}
}

// TestLoginFlow drives the full pipe-based login against a fake gcloud that
// prints the consent URL + prompt, then succeeds once a code is written to
// stdin (and reports a valid token on the follow-up status check).
func TestLoginFlow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not supported on windows")
	}
	// The fake prints the URL and prompt, reads one line from stdin, and — if a
	// non-empty code was supplied — writes the ADC file and exits 0, as gcloud does.
	cfg := t.TempDir()
	marker := filepath.Join(cfg, "application_default_credentials.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", cfg)
	bin := writeFakeGcloud(t, `#!/bin/sh
if [ "$3" = "login" ]; then
  echo "Go to the following link in your browser, and complete the sign-in prompts:"
  echo ""
  echo "    https://accounts.google.com/o/oauth2/auth?response_type=code&client_id=abc&state=xyz"
  echo ""
  printf "Once finished, enter the verification code provided in your browser: "
  read code
  if [ -n "$code" ]; then echo '{"type":"authorized_user"}' > "`+marker+`"; echo "Credentials saved."; exit 0; fi
  exit 1
fi
exit 1
`)
	restore := swapBin(bin)
	defer restore()
	resetLoginLocked()

	if err := startLogin(); err != nil {
		t.Fatalf("startLogin: %v", err)
	}
	// Wait for the URL + prompt to be scraped.
	waitFor(t, 3*time.Second, func() bool {
		s := snapshotLogin()
		return s.VerifyURL != "" && s.AwaitingCode
	})
	if got := snapshotLogin().VerifyURL; !strings.HasPrefix(got, "https://accounts.google.com/o/oauth2/auth?") {
		t.Fatalf("verify URL not scraped: %q", got)
	}

	if err := submitCode("4/0AVerificationCode"); err != nil {
		t.Fatalf("submitCode: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool { return snapshotLogin().Done })
	s := snapshotLogin()
	if !s.Success {
		t.Fatalf("expected success, got %+v", s)
	}
}

func TestSubmitCode_NoFlow(t *testing.T) {
	resetLoginLocked()
	if err := submitCode("abc"); err == nil {
		t.Fatal("expected error submitting code with no flow in progress")
	}
	if err := submitCode(""); err == nil {
		t.Fatal("expected error submitting an empty code")
	}
}

// isolateADC points ADC resolution at an empty gcloud config dir (and clears
// GOOGLE_APPLICATION_CREDENTIALS), so a real credentials file on the test
// machine can't make CheckStatus report signed in.
func isolateADC(t *testing.T) {
	t.Helper()
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", t.TempDir())
}

func swapBin(bin string) func() {
	old := GcloudBin
	GcloudBin = bin
	return func() { GcloudBin = old }
}

func writeFakeGcloud(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "gcloud")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gcloud: %v", err)
	}
	return bin
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}
