// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package daynotes

import "time"

// nowFixed is a stable "now" for tests that only need the pass to have a day
// boundary, not a particular one.
func nowFixed() time.Time {
	return time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC)
}
