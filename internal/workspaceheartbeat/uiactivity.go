// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package workspaceheartbeat

import (
	"net/http"
	"sync/atomic"
	"time"
)

// uiActivityWindow is how long one browser report keeps the workspace marked
// active. The browser reports at most once a minute, and the platform's idle
// timer is measured in tens of minutes, so the window only has to be generous
// enough to survive a few missed reports — precision buys nothing here.
const uiActivityWindow = 5 * time.Minute

// lastUIActivity holds the Unix-nanosecond time of the most recent browser
// report; zero means the web UI has never reported activity.
var lastUIActivity atomic.Int64

// MarkUIActivity records that someone is using the web UI right now.
func MarkUIActivity() { lastUIActivity.Store(time.Now().UnixNano()) }

// UIActive reports whether the web UI saw user interaction inside the current
// window. Reading or typing in the browser counts as using the workspace even
// though no chat turn is running.
func UIActive() bool {
	at := lastUIActivity.Load()
	return at != 0 && time.Since(time.Unix(0, at)) < uiActivityWindow
}

// HandleActivity records a browser-reported interaction. The browser sends no
// payload and needs no response: the only information carried is that the
// request happened at all.
func HandleActivity(w http.ResponseWriter, r *http.Request) {
	MarkUIActivity()
	w.WriteHeader(http.StatusNoContent)
}
