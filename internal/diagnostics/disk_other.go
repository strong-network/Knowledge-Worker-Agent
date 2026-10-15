// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package diagnostics

func diskSpace(string) (free, total uint64) { return 0, 0 }
