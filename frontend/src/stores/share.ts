// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { defineStore } from 'pinia'
import { ref } from 'vue'
import { fetchShareActivity, type ShareActivity } from '../api'
import { useSessionsStore } from './sessions'
import { useUiStore } from './ui'
import { useNotifications } from '../composables/useNotifications'

// Shared chats' live state. The open chat's presence comes from its
// own /events connection; every other shared chat is polled, because the
// owner's tab follows only the chat it has open.
export const useShareStore = defineStore('share', () => {
  const activity = ref<Map<string, ShareActivity>>(new Map())
  const live = ref<{ sid: string; guests: string[] } | null>(null)
  const modalSessionId = ref<string | null>(null)
  let timer: number | undefined

  function present(sid: string): number {
    if (live.value?.sid === sid) return live.value.guests.length
    const a = activity.value.get(sid)
    if (a) return a.present
    return useSessionsStore().sessions.find(s => s.id === sid)?.present || 0
  }

  function setLive(sid: string, guests: string[]) {
    live.value = { sid, guests }
  }

  function clearLive(sid: string) {
    if (live.value?.sid === sid) live.value = null
  }

  async function poll() {
    const sessions = useSessionsStore()
    if (!sessions.sessions.some(s => s.shared)) {
      activity.value = new Map()
      return
    }
    let rows: ShareActivity[]
    try {
      rows = await fetchShareActivity()
    } catch {
      return
    }
    const before = activity.value
    const next = new Map(rows.map(r => [r.session_id, r]))
    for (const r of rows) {
      if (!r.waiting || before.get(r.session_id)?.waiting || r.session_id === sessions.currentSessionId) continue
      const name = sessions.sessions.find(s => s.id === r.session_id)?.name || 'A shared chat'
      useNotifications().notify(`${name} needs your input`, 'A turn in a chat you share is waiting for you.', {
        toast: document.hasFocus(),
        requireBlur: true,
      })
    }
    activity.value = next
  }

  function start(intervalMs = 10000) {
    if (timer) return
    void poll()
    timer = window.setInterval(() => { if (!document.hidden) void poll() }, intervalMs)
  }

  function openModal(sid: string) {
    modalSessionId.value = sid
    useUiStore().openModal('share')
  }

  return { activity, live, modalSessionId, present, setLive, clearLive, poll, start, openModal }
})
