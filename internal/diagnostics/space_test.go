// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package diagnostics

import "testing"

func TestSpaceIsLowBelowFivePercent(t *testing.T) {
	for _, c := range []struct {
		s   Space
		low bool
	}{
		{Space{Free: 49, Total: 1000}, true},
		{Space{Free: 0, Total: 1000}, true},
		{Space{Free: 50, Total: 1000}, false},
		{Space{Free: 900, Total: 1000}, false},
		{Space{Free: 0, Total: 0}, false},
	} {
		if got := c.s.Low(); got != c.low {
			t.Errorf("%+v: Low() = %v, want %v", c.s, got, c.low)
		}
	}
	if s := DiskSpace(); s.Total == 0 || s.Free > s.Total {
		t.Errorf("DiskSpace() = %+v on this machine", s)
	}
}

// A folder that doesn't exist yet is on its parent's disk.
func TestDiskSpaceOfAFolderNotMadeYet(t *testing.T) {
	dir := t.TempDir()
	free, total := diskSpace(dir + "/not/made/yet")
	pf, pt := diskSpace(dir)
	if total == 0 || total != pt || free > total || pf > pt {
		t.Errorf("missing folder: %d of %d free; its parent: %d of %d", free, total, pf, pt)
	}
}
