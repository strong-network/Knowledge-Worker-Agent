// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package workspaceapps

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
)

// HandlePlatformCheck (GET /api/share/platform) reports whether the owner's
// request carried the platform token and what the sidecar returns with it. It
// proves the credential path end to end and stays as a troubleshooting
// probe. Read-only; the token itself is never echoed or logged.
func HandlePlatformCheck(w http.ResponseWriter, r *http.Request) {
	type sidecar struct {
		OK      bool   `json:"ok"`
		Status  int    `json:"status,omitempty"`
		Code    int    `json:"code,omitempty"`
		Message string `json:"message,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	resp := struct {
		OwnerToken bool    `json:"owner_token"`
		Sidecar    sidecar `json:"sidecar"`
		Apps       []App   `json:"apps"`
	}{Apps: []App{}}

	token := OwnerToken(r)
	resp.OwnerToken = token != ""
	if !resp.OwnerToken {
		resp.Sidecar.Error = "request carried no " + ownerTokenHeader + " header; not called"
	} else if apps, err := List(r.Context(), token); err != nil {
		var se *Error
		if errors.As(err, &se) {
			resp.Sidecar.Status, resp.Sidecar.Code, resp.Sidecar.Message = se.Status, se.Code, se.Message
		}
		resp.Sidecar.Error = err.Error()
		log.Printf("[workspaceapps] list failed: %v", err)
	} else {
		resp.Sidecar.OK, resp.Sidecar.Status, resp.Apps = true, http.StatusOK, apps
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(resp)
}
