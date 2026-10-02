// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package files

import (
	"io/fs"
	"strings"
	"time"
)

// TreeBudget caps how many entries one listing looks at to date its folders,
// so a large tree costs a bounded amount on every panel refresh.
const TreeBudget = 10000

// TreeModTime returns the newest modification time of dir and everything under
// it in fsys, spending entries from budget. The file panel shows a folder as
// changed when this moves, so a write deep inside a folder nobody has opened
// still shows. Hidden entries and node_modules are skipped: they are where the
// budget would go, not what anyone is waiting to see change, and a guest never
// sees hidden entries at all.
func TreeModTime(fsys fs.FS, dir string, own time.Time, budget *int) time.Time {
	newest := own
	_ = fs.WalkDir(fsys, dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if *budget <= 0 {
			return fs.SkipAll
		}
		*budget--
		if strings.HasPrefix(d.Name(), ".") || (d.IsDir() && d.Name() == "node_modules") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest
}
