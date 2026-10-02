// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package chat

import (
	"log"
	"sync"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/db"
)

// QueuedPrompt is a prompt waiting to be sent on a session after the current
// (and any earlier-queued) stream finishes.
type QueuedPrompt struct {
	ID         string    `json:"id"`
	Prompt     string    `json:"prompt"`
	EnqueuedAt time.Time `json:"enqueued_at"`
	// Skill is the skill armed when this prompt was queued. It travels
	// with the message it was armed for, so a skill chosen for a queued prompt
	// is not silently re-applied to whatever the user types next.
	Skill string `json:"skill,omitempty"`
	// AuthorID/AuthorName travel with a guest's prompt while it waits.
	AuthorID   string `json:"author_id,omitempty"`
	AuthorName string `json:"author_name,omitempty"`
}

type sessionQueue struct {
	mu    sync.Mutex
	items []QueuedPrompt
}

// queues holds the FIFO of pending prompts per session.
var queues sync.Map // map[sessionID]*sessionQueue

func getQueue(sessionID string) *sessionQueue {
	v, _ := queues.LoadOrStore(sessionID, &sessionQueue{})
	return v.(*sessionQueue)
}

// EnqueuePrompt appends a prompt to the session's queue and returns the new
// item. The prompt is *not* sent immediately; callers should use
// MaybeStartNext (or rely on dispatcher chaining) to drain the queue.
func EnqueuePrompt(sessionID, prompt string, turn ...Turn) QueuedPrompt {
	q := getQueue(sessionID)
	q.mu.Lock()
	defer q.mu.Unlock()
	t := firstTurn(turn)
	item := QueuedPrompt{
		ID:         NewUUID(),
		Prompt:     prompt,
		EnqueuedAt: time.Now().UTC(),
		Skill:      t.Skill,
		AuthorID:   t.Author.ID,
		AuthorName: t.Author.Name,
	}
	q.items = append(q.items, item)
	queueChanged(sessionID)
	return item
}

// SnapshotQueue returns a copy of the session's pending queue.
func SnapshotQueue(sessionID string) []QueuedPrompt {
	q := getQueue(sessionID)
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]QueuedPrompt, len(q.items))
	copy(out, q.items)
	return out
}

// RemoveQueuedAt removes the item at the given index. Returns true if removed.
func RemoveQueuedAt(sessionID string, idx int) bool {
	q := getQueue(sessionID)
	q.mu.Lock()
	defer q.mu.Unlock()
	if idx < 0 || idx >= len(q.items) {
		return false
	}
	q.items = append(q.items[:idx], q.items[idx+1:]...)
	queueChanged(sessionID)
	return true
}

// RemoveQueuedByID removes the item with the given id. Returns true if removed.
func RemoveQueuedByID(sessionID, id string) bool {
	q := getQueue(sessionID)
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, it := range q.items {
		if it.ID == id {
			q.items = append(q.items[:i], q.items[i+1:]...)
			queueChanged(sessionID)
			return true
		}
	}
	return false
}

// ClearQueue empties the queue for a session.
func ClearQueue(sessionID string) {
	q := getQueue(sessionID)
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = nil
	queueChanged(sessionID)
}

// DropGuestPrompts removes every prompt a guest queued, leaving the owner's,
// and returns how many went. Stopping sharing uses it.
func DropGuestPrompts(sessionID string) int {
	q := getQueue(sessionID)
	q.mu.Lock()
	defer q.mu.Unlock()
	kept := q.items[:0]
	for _, it := range q.items {
		if it.AuthorID == "" {
			kept = append(kept, it)
		}
	}
	dropped := len(q.items) - len(kept)
	q.items = kept
	if dropped > 0 {
		queueChanged(sessionID)
	}
	return dropped
}

// QueueLen returns the number of pending prompts for a session.
func QueueLen(sessionID string) int {
	q := getQueue(sessionID)
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// popNext removes and returns the next queued prompt, if any.
func popNext(sessionID string) (QueuedPrompt, bool) {
	q := getQueue(sessionID)
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return QueuedPrompt{}, false
	}
	item := q.items[0]
	q.items = q.items[1:]
	queueChanged(sessionID)
	return item, true
}

// SendOrEnqueue is the canonical entry point for the HTTP "send a message"
// path. If the session has an in-flight stream, the prompt is appended to the
// queue and (nil, queued=true) is returned. Otherwise the prompt is persisted
// as a user message and a new stream is started.
//
// turn carries per-message settings that are not persisted with the session
// (a skill forced from the composer); it follows the prompt down either branch.
func SendOrEnqueue(sessionID, prompt string, cfg config.SessionConfig, turn ...Turn) (stream *Stream, queued *QueuedPrompt) {
	if IsStreaming(sessionID) {
		item := EnqueuePrompt(sessionID, prompt, turn...)
		log.Printf("[QUEUE] session=%s enqueued (queue_len=%d) prompt_bytes=%d",
			sessionID, QueueLen(sessionID), len(prompt))
		return nil, &item
	}
	t := firstTurn(turn)
	t.ID = NewUUID()
	db.AddUserMessage(sessionID, prompt, t.Author.ID, t.Author.Name, t.ID)
	return StartChatStream(sessionID, prompt, cfg, t), nil
}

// StartNextQueued drains the next queued prompt for a session (if any) and
// starts a new stream for it. Called by the dispatcher when the previous turn
// completes and externally as a safety net (e.g. after a stream errors).
func StartNextQueued(sessionID string) *Stream {
	if IsStreaming(sessionID) {
		return nil
	}
	item, ok := popNext(sessionID)
	if !ok {
		return nil
	}
	cfg, err := db.GetSessionConfig(sessionID)
	if err != nil {
		log.Printf("[QUEUE] session=%s cannot drain queue: %v", sessionID, err)
		return nil
	}
	log.Printf("[QUEUE] session=%s draining next prompt (remaining=%d) prompt_bytes=%d",
		sessionID, QueueLen(sessionID), len(item.Prompt))
	t := Turn{
		Skill:  item.Skill,
		Author: Author{ID: item.AuthorID, Name: item.AuthorName},
		ID:     NewUUID(),
	}
	db.AddUserMessage(sessionID, item.Prompt, item.AuthorID, item.AuthorName, t.ID)
	return StartChatStream(sessionID, item.Prompt, cfg, t)
}

// resetQueuesForTest clears the in-memory queue map. Test-only helper.
func resetQueuesForTest() {
	queues.Range(func(k, _ any) bool {
		queues.Delete(k)
		return true
	})
}
