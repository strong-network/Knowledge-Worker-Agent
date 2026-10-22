// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package diagnostics

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/layout"
)

// Disk is free space on the disk holding the Knowledge Worker Agent folder,
// and the size of the folders that grow.
type Disk struct {
	Path       string     `json:"path"`
	FreeBytes  uint64     `json:"free_bytes"`
	TotalBytes uint64     `json:"total_bytes"`
	Items      []DiskItem `json:"items"`
}

type DiskItem struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	// Partial is set when the walk ran out of time; Bytes is then a minimum.
	Partial bool `json:"partial"`
}

// sizeBudget bounds each walk, so a large folder can't stall the dialog.
const sizeBudget = 4 * time.Second

// LowSpaceShare is the share of the disk below which free space is low.
const LowSpaceShare = 0.05

// Space is free and total space on the disk holding the Knowledge Worker Agent folder.
type Space struct{ Free, Total uint64 }

// DiskSpace measures it without walking any folder, so it's cheap to poll.
func DiskSpace() Space {
	free, total := diskSpace(layout.Root())
	return Space{Free: free, Total: total}
}

// Low reports whether less than LowSpaceShare of the disk is free. An unknown size isn't low.
func (s Space) Low() bool {
	return s.Total > 0 && float64(s.Free) < LowSpaceShare*float64(s.Total)
}

// CollectDisk measures the disk. It walks folders, so it is slower than Collect.
func CollectDisk() Disk {
	root, system := layout.Root(), layout.System()
	d := Disk{Path: root, Items: []DiskItem{}}
	d.FreeBytes, d.TotalBytes = diskSpace(root)
	// One walk covers the folder and its system files, which live inside it.
	total, sub, partial := pathSize(root, system, time.Now().Add(sizeBudget))
	d.Items = append(d.Items,
		DiskItem{Label: "Knowledge Worker Agent folder", Path: root, Bytes: total, Partial: partial},
		DiskItem{Label: "System files (logs, configuration, add-ons)", Path: system, Bytes: sub, Partial: partial})
	if config.DBPath != "" {
		n, _, _ := pathSize(config.DBPath, "", time.Now().Add(sizeBudget))
		d.Items = append(d.Items, DiskItem{Label: "Database", Path: config.DBPath, Bytes: n})
	}
	if dir := opencodeDataDir(); dir != "" {
		n, _, partial := pathSize(dir, "", time.Now().Add(sizeBudget))
		d.Items = append(d.Items, DiskItem{Label: "opencode data (chat history, logs)", Path: dir, Bytes: n, Partial: partial})
	}
	return d
}

// pathSize returns the size of a file or folder, the part of it under sub
// (when sub is inside it), and whether the deadline cut the walk short.
// Symlinks are not followed.
func pathSize(path, sub string, deadline time.Time) (total, subTotal int64, partial bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0, false
	}
	if !info.IsDir() {
		return info.Size(), 0, false
	}
	prefix := ""
	if sub != "" {
		prefix = filepath.Clean(sub) + string(filepath.Separator)
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if time.Now().After(deadline) {
			partial = true
			return fs.SkipAll
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				total += fi.Size()
				if prefix != "" && strings.HasPrefix(p, prefix) {
					subTotal += fi.Size()
				}
			}
		}
		return nil
	})
	return total, subTotal, partial
}
