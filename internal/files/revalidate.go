// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package files

import "net/http"

// Revalidate makes the browser check with the server before reusing a file it
// fetched earlier. Files change on disk under the same URL, and without this a
// browser may reuse its copy for as long as a tenth of the file's age. An
// unchanged file still costs only a 304.
func Revalidate(h http.Header) {
	h.Set("Cache-Control", "no-cache")
}
