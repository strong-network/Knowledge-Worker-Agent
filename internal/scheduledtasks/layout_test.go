// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package scheduledtasks

import (
	"path/filepath"
	"testing"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout/layouttest"
)

func TestTaskWorkdirBeforeAndAfterTheMigration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir, err := provisionTaskWorkdir("Weekly report", "task_0123456789")
	if err != nil {
		t.Fatalf("before: %v", err)
	}
	if filepath.Dir(dir) != filepath.Join(home, "Scheduled Tasks") {
		t.Errorf("before: %q is not in the home folder's Scheduled Tasks", dir)
	}

	root := layouttest.Migrated(t)
	dir, err = provisionTaskWorkdir("Weekly report", "task_0123456789")
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if filepath.Dir(dir) != filepath.Join(root, "Scheduled Tasks") {
		t.Errorf("after: %q is not in the root's Scheduled Tasks", dir)
	}
}
