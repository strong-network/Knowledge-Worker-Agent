// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { ref, watch, onMounted, onBeforeUnmount } from 'vue'
import type { Ref } from 'vue'
import { fetchGitStatus, gitPull, gitPush, gitFetch } from '../api'
import type { GitStatus } from '../api'

// useGitSync polls `git status` for the supplied path while the consuming
// component is mounted, exposing reactive status + pull/push helpers. Used
// from both the chat header (workspace-level) and the file manager modal
// (cwd-level).
//
// Options:
//   - intervalMs: local `git status` poll cadence (default 8000).
//   - fetchIntervalMs: when set (>0), also periodically `git fetch` the remote
//     so status.behind reflects the LIVE remote (for pull suggestions). Off by
//     default so plain-status consumers don't hit the network.
//   - fetchEnabled: optional reactive gate for the periodic fetch (e.g. only in
//     a project chat). When it returns false, the fetch tick is skipped.
export function useGitSync(
  path: Ref<string>,
  opts: { intervalMs?: number; fetchIntervalMs?: number; fetchEnabled?: () => boolean } = {},
) {
  const status = ref<GitStatus | null>(null)
  const busy = ref(false)
  const log = ref('')

  async function refresh() {
    if (!path.value) {
      status.value = null
      return
    }
    try {
      status.value = await fetchGitStatus(path.value)
    } catch {
      status.value = null
    }
  }

  // remoteFetch contacts the remote and updates status.behind with the live
  // position. Best-effort and silent (no toast/log) so the periodic check
  // doesn't spam the user; it only powers the "pull?" suggestion.
  async function remoteFetch() {
    if (!path.value || busy.value) return
    if (opts.fetchEnabled && !opts.fetchEnabled()) return
    if (status.value && (!status.value.is_repo || !status.value.has_remote)) return
    try {
      const res = await gitFetch(path.value)
      if (res?.status) status.value = res.status
    } catch {
      /* ignore transient network/auth failures */
    }
  }

  async function run(op: 'pull' | 'push') {
    if (!path.value || busy.value) return
    busy.value = true
    log.value = `Running git ${op} ...`
    try {
      const fn = op === 'pull' ? gitPull : gitPush
      const res = await fn(path.value)
      status.value = res.status
      const banner = res.success
        ? `✓ git ${op} succeeded`
        : `✗ git ${op} failed${res.error ? ': ' + res.error : ''}`
      log.value = `${banner}\n\n${(res.output || '').trim()}`
      ;(window as any).showToast?.(banner)
    } catch (e) {
      log.value = `✗ git ${op} error: ${e instanceof Error ? e.message : String(e)}`
    } finally {
      busy.value = false
    }
  }

  watch(path, () => { log.value = ''; refresh(); if (opts.fetchIntervalMs) remoteFetch() })

  let handle: number | null = null
  let fetchHandle: number | null = null
  onMounted(() => {
    refresh()
    handle = window.setInterval(() => {
      if (!busy.value) refresh()
    }, opts.intervalMs ?? 8000)
    if (opts.fetchIntervalMs && opts.fetchIntervalMs > 0) {
      remoteFetch()
      fetchHandle = window.setInterval(remoteFetch, opts.fetchIntervalMs)
    }
  })
  onBeforeUnmount(() => {
    if (handle !== null) window.clearInterval(handle)
    if (fetchHandle !== null) window.clearInterval(fetchHandle)
  })

  return {
    status,
    busy,
    log,
    refresh,
    remoteFetch,
    pull: () => run('pull'),
    push: () => run('push'),
  }
}
