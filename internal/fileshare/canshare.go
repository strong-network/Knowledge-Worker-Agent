// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package fileshare

import (
	"net/http"
	"path/filepath"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/fileshare/check"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/fileshare/viewer"
)

type canShareResponse struct {
	CanShare bool `json:"can_share"`
	// Reason says why files there can't be shared, in words.
	Reason string `json:"reason,omitempty"`
	// For a file: whether it's self-contained and what the check finds, or
	// why it couldn't be checked.
	SelfContained *bool           `json:"self_contained,omitempty"`
	Findings      []check.Finding `json:"findings,omitempty"`
	Problem       string          `json:"problem,omitempty"`
}

// HandleCanShare serves GET /api/artifacts/can-share?folder=&file=: whether
// files in a chat's folder can be shared now and, for a file there, what the
// check finds. The agent asks it once it has made a report, so it offers
// sharing only where the owner can share, and fixes what the check would
// refuse. The agent knows the folder it works in, not its chat's ID.
func HandleCanShare(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	where := q.Get("folder")
	if !filepath.IsAbs(where) {
		writeFailure(w, &failure{http.StatusBadRequest, "Give the chat's folder as an absolute path."})
		return
	}
	folder, shareable, found := chatFolder(filepath.Clean(where))
	resp := canShareResponse{CanShare: true}
	ps, err := ports()
	switch {
	case err != nil:
		resp.CanShare, resp.Reason = false, err.Error()
	case len(ps) == 0:
		resp.CanShare, resp.Reason = false, "Sharing files is turned off in this workspace."
	case !found:
		resp.CanShare, resp.Reason = false, "This folder isn't a chat's folder."
	case !shareable:
		resp.CanShare, resp.Reason = false, "Files in a temporary chat can't be shared. Keep the chat first."
	}
	if name := q.Get("file"); name != "" {
		checkFor(&resp, folder, found, filepath.Clean(where), name)
	}
	writeJSON(w, http.StatusOK, resp)
}

// chatFolder finds the chat folder that path is in: the nearest one at or
// above it. shareable is false when only temporary chats work there.
func chatFolder(path string) (folder string, shareable, found bool) {
	for dir, keep := range db.ChatFolders() {
		if _, in := inside(dir, path); in && len(dir) > len(folder) {
			folder, shareable, found = dir, keep, true
		}
	}
	return folder, shareable, found
}

// checkFor checks a file in the chat's folder, named from where or by its
// absolute path, as sharing would.
func checkFor(resp *canShareResponse, folder string, found bool, where, name string) {
	if !found {
		resp.Problem = "Only a file in a chat's folder can be checked."
		return
	}
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(where, name)
	}
	rel, in := inside(folder, filepath.Clean(path))
	if !in || rel == "" {
		resp.Problem = "Only a file in a chat's folder can be checked."
		return
	}
	f, _, err := viewer.Open(folder, rel)
	if err != nil {
		resp.Problem = err.Error()
		return
	}
	res, err := checked(f, rel)
	f.Close()
	if err != nil {
		resp.Problem = err.Error()
		return
	}
	ok := !res.Blocking
	resp.SelfContained, resp.Findings = &ok, res.Findings
}
