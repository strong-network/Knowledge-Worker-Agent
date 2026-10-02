// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import type { Session } from '../api'

/**
 * The chat-delete confirmation, in one place so both call sites ask the same
 * question.
 *
 * Day notes survive deletion of the chat they describe, so that clearing
 * out an old chat does not erase the record of what was done that month. That
 * default is right for routine cleanup — but someone deleting a chat *because
 * of what is in it* (a pasted credential, a personal matter, something said in
 * error) would find a summary of it still there afterwards. A retention default
 * the user cannot override at the moment of deletion is not a default, it is a
 * trap.
 *
 * So when notes exist the confirmation states that they are kept and offers to
 * remove them in the same action. When there are none — every workspace that
 * has not enabled day notes — nothing extra is asked.
 */
export function confirmChatDelete(
  session: Pick<Session, 'note_count' | 'shared' | 'guest_count'> | undefined,
  prompt = 'Delete this chat?',
): { ok: boolean; deleteNotes: boolean } {
  // Deleting a shared chat revokes other people's access, so the
  // confirmation says so. It counts who opened it and names no one.
  if (session?.shared) {
    prompt += `\n\nIt is currently shared${openedBy(session.guest_count || 0, ', and has been')}. ` +
      'Everyone you shared it with will lose access immediately, and the chat and its history will be deleted.'
  }
  if (!confirm(prompt)) return { ok: false, deleteNotes: false }

  const notes = session?.note_count || 0
  if (notes < 1) return { ok: true, deleteNotes: false }

  const deleteNotes = confirm(
    `This chat has ${notes} day note${notes === 1 ? '' : 's'} summarising what was worked on.\n\n` +
      'Notes are kept by default, so deleting old chats does not erase your history — ' +
      'they stay searchable and are marked as describing a deleted chat.\n\n' +
      'OK — delete the notes too.\n' +
      'Cancel — keep the notes.',
  )
  return { ok: true, deleteNotes }
}

function openedBy(n: number, lead: string): string {
  return n > 0 ? `${lead} opened by ${n} ${n === 1 ? 'person' : 'people'}` : ''
}

/** The stop-sharing confirmation. guestCount is how many opened it. */
export function confirmStopSharing(guestCount: number): boolean {
  const opened = openedBy(guestCount, ' It has been')
  return confirm(
    'Stop sharing?\n\nEveryone you shared this with will lose access to this chat immediately.' +
      (opened ? `${opened}.` : ''),
  )
}
