// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// blank-text-guard — keeps a blank text block in a chat's history from failing every later turn.
//
// OpenCode stores a reply's text as it streams in, so a reply of only blank lines leaves a
// whitespace-only text block. Every turn replays the history, and the Claude models behind
// github-copilot reject such a block ("messages: text content blocks must contain
// non-whitespace text"), whatever the new message says. OpenCode only drops text that is exactly
// "", and only for the Anthropic provider. This leaves the block out of each request instead;
// what's stored doesn't change, so chats that already hold one work again.

function blank(part) {
  return part?.type === "text" && typeof part.text === "string" && part.text !== "" && part.text.trim() === ""
}

export const BlankTextGuard = async () => {
  return {
    "experimental.chat.messages.transform": async (_input, output) => {
      // Never throw: a failing hook would fail the turn it is there to save.
      try {
        let dropped = 0
        for (const message of output?.messages ?? []) {
          const parts = message?.parts
          if (!Array.isArray(parts)) continue
          for (let i = parts.length - 1; i >= 0; i--) {
            if (blank(parts[i])) {
              // In place: OpenCode builds the request from these same arrays.
              parts.splice(i, 1)
              dropped++
            }
          }
        }
        if (dropped) console.error(`[blank-text-guard] left ${dropped} whitespace-only text block(s) out of the request`)
      } catch (err) {
        console.error("[blank-text-guard] failed to inspect the history:", err)
      }
    },
  }
}
