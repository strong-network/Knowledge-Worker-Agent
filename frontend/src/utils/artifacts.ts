// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import type { Artifact, ArtifactFinding, ArtifactStatus, FileEntry, ShareAudience } from '../api'

// The types that can be shared, as the server's viewer serves them.
const SHAREABLE = /\.(html?|md|markdown|pdf|png|jpe?g|gif|webp|svg)$/i

// Whether a file inside root can be shared: a type the viewer serves, and no
// hidden part in its path below root.
export function canShareFile(root: string, entry: FileEntry): boolean {
  if (entry.is_dir || !SHAREABLE.test(entry.name)) return false
  const prefix = root.endsWith('/') ? root : root + '/'
  if (!entry.path.startsWith(prefix)) return false
  return !entry.path.slice(prefix.length).split('/').some(p => p.startsWith('.'))
}

export const STATUS_LABEL: Record<ArtifactStatus, string> = {
  shared: 'Shared',
  off: 'Turned off',
  attention: 'Needs attention',
  missing: 'Missing',
  unavailable: 'Unavailable',
  removed: 'Removed',
  fixing: 'Fixing',
}

// Who can open it, in words. Null: the platform wasn't asked.
export function audienceLabel(a: ShareAudience | null): string {
  if (!a) return 'Shared'
  if (a.project) return 'Shared with everyone in the project'
  const n = a.user_ids.length
  if (!n) return 'Shared, but only you can open it'
  return `Shared with ${n} ${n === 1 ? 'person' : 'people'}`
}

// The Files widget's marker for a shared file: who can open it, or what's wrong.
export function markerFor(a: Artifact | undefined): { label: string; attention: boolean } | null {
  if (!a) return null
  if (a.status !== 'shared') return { label: `${STATUS_LABEL[a.status]}: ${a.problem || ''}`.trim(), attention: true }
  return { label: audienceLabel(a.audience), attention: false }
}

export function findingText(f: ArtifactFinding): string {
  const more = f.more ? `, and ${f.more} more ${f.more === 1 ? 'place' : 'places'}` : ''
  return `${f.what} (line ${f.line}${more})`
}
