// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

export const HOME_PATH = '/home/developer'

export function formatDisplayPath(path: string): string {
  if (!path) return ''
  const normalized = path.replace(/\/+$/, '') || '/'
  if (normalized === HOME_PATH) return '~'
  if (normalized.startsWith(`${HOME_PATH}/`)) {
    return `~/${normalized.slice(HOME_PATH.length + 1)}`
  }
  return normalized
}
