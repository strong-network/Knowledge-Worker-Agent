// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package scheduledtasks

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// A task works only in the home folder: it can't be given a hidden folder
// directly in the home folder, or one outside it.
func TestATasksFolderStaysInTheHomeFolder(t *testing.T) {
	setupTasksDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	reports := filepath.Join(home, "Reports")
	if err := os.Mkdir(reports, 0o755); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&Handlers{}).RegisterRoutes(mux)
	create := func(workdir string) int {
		body := `{"prompt":"Summarise the week","first_run_at":"2030-01-07T09:00:00Z","workdir":"` + workdir + `"}`
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/scheduled-tasks", strings.NewReader(body)))
		return w.Code
	}
	for _, dir := range []string{filepath.Join(home, ".ssh"), "/etc"} {
		if code := create(dir); code != http.StatusBadRequest {
			t.Errorf("a task in %s: %d, want 400", dir, code)
		}
	}
	if n := len(db.ListScheduledTasks()); n != 0 {
		t.Fatalf("%d tasks saved from refused requests", n)
	}
	if code := create(reports); code != http.StatusCreated {
		t.Fatalf("a task in %s: %d, want 201", reports, code)
	}
	if tasks := db.ListScheduledTasks(); len(tasks) != 1 || tasks[0].Workdir != reports {
		t.Errorf("saved tasks: %+v", tasks)
	}
}
