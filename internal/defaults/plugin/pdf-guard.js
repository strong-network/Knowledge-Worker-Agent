// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// pdf-guard — keeps an unreadable attachment from bricking a chat session.
//
// The problem it solves:
//
// OpenCode's `read` tool attaches PDFs and images to the conversation as
// binary file parts (`{type:"file", mime:"application/pdf", url:"data:…"}`).
// It does this unconditionally, without asking whether the current model's
// provider can accept that media type. The github-copilot provider — the only
// one this product authenticates, and the one behind every model we offer —
// only accepts `image/*` parts. Anything else makes the AI SDK throw:
//
//     'file part media type application/pdf' functionality not supported.
//
// That alone would just be a failed turn. What makes it serious is that the
// attachment is persisted into OpenCode's own session history, and every later
// turn replays the whole history. So the failure is permanent: once a PDF has
// been read, even a plain "hi" in that session fails forever.
//
// This hook runs after every tool call and drops attachments the provider
// cannot accept, before they are stored. The turn then completes normally and
// the session stays usable.
//
// Why the allowlist is `image/*` rather than a PDF denylist: the two failure
// modes are not symmetric. Stripping an attachment the provider could have
// handled costs a bit of capability and says so out loud. Keeping one it
// cannot handle costs the user their entire session, silently and
// irreversibly. So anything not known-good is dropped. If a provider that
// accepts PDFs is ever added, widen ACCEPTED_MIME_PREFIXES — do not remove the
// guard.

const ACCEPTED_MIME_PREFIXES = ["image/"]

// Message left in place of the file. It is written for the model, not the
// user: it has to stop the assistant inventing contents for a file it never
// received, and give it something useful to do instead.
//
// That "something useful" is now the `pdf` skill, which reads PDFs the way
// that actually works here — extracting text with a script, or rendering a
// page to PNG (which passes this guard, since it is an image). The guard is
// therefore no longer a dead end for PDFs; it is a redirect.
//
// The skill instruction is conditional on purpose. The guard ships in every
// deployment, but the `pdf` skill is content, and content differs: a
// centrally-configured workspace has it, one running only the bundled
// defaults may not. The model can see its own available skills, so let it
// decide, and keep the old advice as the fallback rather than promising a
// capability that might not be installed.
function strippedNotice(names, mimes) {
  const list = names.length ? ` (${names.join(", ")})` : ""
  const isPdf = mimes.some((m) => String(m).toLowerCase().includes("pdf"))
  return [
    `The file${names.length === 1 ? "" : "s"}${list} could not be shown to the model.`,
    "This model cannot read PDFs or other non-image attachments — only images.",
    "The file itself is fine and is still on disk; nothing was modified.",
    "Do not guess or invent the contents.",
    isPdf
      ? "Use the `pdf` skill to read it: load that skill and follow it to extract the text, or to render the pages to PNG and read those."
      : "If a skill for this file type is available, load it and follow it.",
    "If no such skill is available, tell the user the document cannot be read directly in chat,",
    "and suggest converting it to text or images first, or pasting the relevant part.",
  ].join(" ")
}

function accepted(mime) {
  return typeof mime === "string" && ACCEPTED_MIME_PREFIXES.some((p) => mime.startsWith(p))
}

export const PdfGuard = async () => {
  return {
    "tool.execute.after": async (input, output) => {
      // Never let this hook throw: an exception here would fail a tool call
      // that was otherwise fine. Degrading to "no guard" is bad, but breaking
      // every tool call is worse.
      try {
        const attachments = output?.attachments
        if (!Array.isArray(attachments) || attachments.length === 0) return

        const kept = attachments.filter((a) => accepted(a?.mime))
        if (kept.length === attachments.length) return

        const dropped = attachments.filter((a) => !accepted(a?.mime))
        const names = dropped
          .map((a) => a?.filename || a?.name || a?.mime)
          .filter(Boolean)
        const mimes = dropped.map((a) => a?.mime).filter(Boolean)

        // Mutating `output` in place is the contract: OpenCode passes the same
        // object to this hook and then persists it.
        output.attachments = kept
        output.output = `${output.output ?? ""}\n\n${strippedNotice(names, mimes)}`.trim()

        console.error(
          `[pdf-guard] dropped ${dropped.length} unsupported attachment(s) from tool '${input?.tool}': ` +
            dropped.map((a) => a?.mime).join(", "),
        )
      } catch (err) {
        console.error("[pdf-guard] failed to inspect tool output:", err)
      }
    },
  }
}
