// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { defineStore } from 'pinia'
import { ref } from 'vue'
import * as api from '../api'
import type { ChatSearchHit, SearchTerm } from '../api'
import { useSessionsStore } from './sessions'
import { useChatStore } from './chat'
import { useUiStore } from './ui'

export type SearchRange = 'any' | 'week' | 'month' | 'year' | 'custom'

// A search result being opened: the chat shows its message and the words.
export interface SearchFocus {
  sessionId: string
  messageId?: number
  terms: SearchTerm[]
  // Still to scroll to the message.
  pending: boolean
}

const PAGE = 20

// The Search page keeps its query and results, so coming back from a result
// shows them again.
export const useSearchStore = defineStore('search', () => {
  const query = ref('')
  const range = ref<SearchRange>('any')
  const customFrom = ref('') // YYYY-MM-DD, local
  const customTo = ref('')
  const chats = ref<ChatSearchHit[]>([])
  const total = ref(0)
  const match = ref<'all' | 'any'>('all')
  const terms = ref<SearchTerm[]>([])
  const variants = ref<string[]>([])
  const searched = ref('') // the query the results are for
  const loading = ref(false)
  const error = ref('')
  const focus = ref<SearchFocus | null>(null)
  let seq = 0

  function bounds(): { from?: string; to?: string } {
    const now = new Date()
    const ago = (f: (d: Date) => void) => { const d = new Date(now); f(d); return d.toISOString() }
    switch (range.value) {
      case 'week': return { from: ago(d => d.setDate(d.getDate() - 7)) }
      case 'month': return { from: ago(d => d.setMonth(d.getMonth() - 1)) }
      case 'year': return { from: ago(d => d.setFullYear(d.getFullYear() - 1)) }
      case 'custom': {
        // Whole local days.
        const day = (v: string, end: boolean) =>
          v ? new Date(`${v}T${end ? '23:59:59.999' : '00:00:00'}`).toISOString() : undefined
        return { from: day(customFrom.value, false), to: day(customTo.value, true) }
      }
    }
    return {}
  }

  async function run(more = false) {
    const q = query.value.trim()
    const mine = ++seq
    if (!q) {
      chats.value = []
      total.value = 0
      searched.value = ''
      error.value = ''
      loading.value = false
      return
    }
    loading.value = true
    error.value = ''
    try {
      const res = await api.searchChats({ q, ...bounds(), offset: more ? chats.value.length : 0, limit: PAGE })
      if (mine !== seq) return
      chats.value = more ? [...chats.value, ...res.chats] : res.chats
      total.value = res.total
      match.value = res.match
      terms.value = res.terms
      variants.value = res.variants
      searched.value = q
    } catch (e) {
      if (mine === seq) error.value = e instanceof Error ? e.message : String(e)
    } finally {
      if (mine === seq) loading.value = false
    }
  }

  // The words to highlight for a result: a close match's typo variants too.
  function termsFor(hit: ChatSearchHit): SearchTerm[] {
    return hit.exact ? terms.value : [...terms.value, ...variants.value.map(text => ({ text }))]
  }

  async function open(hit: ChatSearchHit) {
    const sessions = useSessionsStore()
    const ui = useUiStore()
    focus.value = { sessionId: hit.session_id, messageId: hit.message?.id, terms: termsFor(hit), pending: !!hit.message }
    if (!sessions.sessions.some(s => s.id === hit.session_id)) await sessions.loadSessions()
    ui.showChat()
    await sessions.switchSession(hit.session_id)
    // Messages this tab watched arrive without ids until the history is reloaded.
    const id = hit.message?.id
    if (id && !(sessions.messages.get(hit.session_id) || []).some(m => m.id === id)) {
      await useChatStore().reloadHistory(hit.session_id)
    }
  }

  return {
    query, range, customFrom, customTo, chats, total, match, terms, variants, searched, loading, error, focus,
    run, termsFor, open,
  }
})
