// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package guest

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Every guest prompt spends the owner's model allowance while the owner may
// not be watching, so each shared chat has a budget: a burst, then one prompt
// back every refill interval. It is kept per chat, not per guest id, because a
// guest mints a new id by clearing cookies but cannot mint a new chat.
var (
	promptBurst  = 10
	promptRefill = 30 * time.Second
	clock        = time.Now
)

type budget struct {
	tokens float64
	at     time.Time
}

var (
	budgetsMu sync.Mutex
	budgets   = map[string]*budget{}
)

// spendPrompt takes a prompt from the chat's budget, or says when one is back.
func spendPrompt(sid string) (bool, time.Duration) {
	budgetsMu.Lock()
	defer budgetsMu.Unlock()
	now := clock()
	b := budgets[sid]
	if b == nil {
		b = &budget{tokens: float64(promptBurst), at: now}
		budgets[sid] = b
	}
	b.tokens = math.Min(float64(promptBurst), b.tokens+now.Sub(b.at).Seconds()/promptRefill.Seconds())
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) * float64(promptRefill))
}

func refuseForBudget(w http.ResponseWriter, wait time.Duration) {
	secs := int(math.Ceil(wait.Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error":       "Guests have sent a lot of messages in this chat in a short time. Try again in " + strconv.Itoa(secs) + " seconds.",
		"retry_after": secs,
	})
}
