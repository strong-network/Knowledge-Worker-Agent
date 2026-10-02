// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Slash commands handled by the web UI. Note: many CLI-shell-only commands
// (/compact, /sessions, /usage, /login, /theme, etc.) cannot work in this web
// UI and have been intentionally removed — they would otherwise be sent as
// plain prompts and the LLM would just chat about them.
export const SLASH_COMMANDS = [
  // Session (handled locally by the web UI)
  { cmd: '/new', desc: 'Start a new conversation', icon: '🆕' },
  { cmd: '/clear', desc: 'Abandon this session and start fresh', icon: '🧹' },
  { cmd: '/undo', desc: 'Rewind last turn and revert file changes', icon: '↩️' },
  { cmd: '/rewind', desc: 'Rewind last turn and revert file changes', icon: '⏪' },
  { cmd: '/copy', desc: 'Copy the last response', icon: '📋' },
  { cmd: '/export', desc: 'Export session as markdown', icon: '📤' },

  // Settings / pickers (open a modal)
  { cmd: '/model', desc: 'Select AI model', icon: '🤖' },
  { cmd: '/agent', desc: 'Browse and select an agent', icon: '🧑‍🚀' },
  { cmd: '/skill', desc: 'Use a specific skill for the next message', icon: '🛠️' },
  { cmd: '/mcp', desc: 'Manage MCP server configuration', icon: '🔌' },

  // Prompt templates — expand into a regular prompt then send to the agent
  { cmd: '/diff', desc: 'Review changes in the current directory', icon: '📝' },
  { cmd: '/review', desc: 'Run code review on current changes', icon: '🔍' },
  { cmd: '/pr', desc: 'Operate on PRs for current branch', icon: '🔀' },
  { cmd: '/plan', desc: 'Create an implementation plan', icon: '🗺️' },
  { cmd: '/research', desc: 'Run a deep research investigation', icon: '🔬' },
  { cmd: '/ask', desc: 'Ask a quick side question (no history)', icon: '❔' },
  { cmd: '/init', desc: 'Initialize agent instructions for repo', icon: '✨' },

  // Help
  { cmd: '/help', desc: 'Show help & shortcuts', icon: '❓' },
]
