// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Shared helpers for the agent's "Plan & progress" todo checklist. The todo
// list arrives as the input of a `todowrite`/`TodoWrite` tool call. These are
// used both inline (MessageBubble) and by the floating GoalsPanel.

export type TodoItem = {
  content: string
  status: 'pending' | 'in_progress' | 'completed' | 'cancelled' | string
  priority?: 'low' | 'medium' | 'high' | string
}

export function isTodoTool(name?: string): boolean {
  if (!name) return false
  const n = name.toLowerCase()
  return n === 'todowrite' || n === 'todo_write' || n === 'todoread' || n === 'todo_read'
}

// Tries multiple shapes — the todo list arrives as `{todos: [...]}`, where
// items may use `content` / `title` / `text` for the text.
export function parseTodos(raw?: string): TodoItem[] | null {
  if (!raw) return null
  let obj: any = raw
  if (typeof raw === 'string') {
    try { obj = JSON.parse(raw) } catch { return null }
  }
  const list = obj?.todos || obj?.items || obj?.list || (Array.isArray(obj) ? obj : null)
  if (!Array.isArray(list)) return null
  const out: TodoItem[] = []
  for (const it of list) {
    if (!it || typeof it !== 'object') continue
    const content = it.content || it.title || it.text || it.name
    if (typeof content !== 'string') continue
    out.push({
      content,
      status: String(it.status || 'pending').toLowerCase(),
      priority: it.priority ? String(it.priority).toLowerCase() : undefined,
    })
  }
  return out.length ? out : null
}

export function todoIcon(status: string): string {
  switch (status) {
    case 'in_progress': return '▶'
    case 'completed':
    case 'done':       return '✓'
    case 'cancelled':
    case 'canceled':   return '✗'
    default:           return '○'
  }
}

export function todoCounts(items: TodoItem[]): { done: number; total: number; active?: TodoItem } {
  const done = items.filter(t => t.status === 'completed' || t.status === 'done').length
  const active = items.find(t => t.status === 'in_progress')
  return { done, total: items.length, active }
}
