// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package entra

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

const (
	// refreshBefore is how long before an access token expires it's replaced.
	refreshBefore = 10 * time.Minute
	// busyLimit is how close to expiry a refresh still waits for a running
	// turn: a new token makes opencode reconnect the connector, which could cut
	// a call in progress.
	busyLimit = 2 * time.Minute
	tick      = time.Minute
)

var startOnce sync.Once

// StartRefresher keeps every Microsoft sign-in fresh while the server runs.
// busy reports whether a turn is running.
func StartRefresher(busy func() bool) {
	startOnce.Do(func() {
		go func() {
			restore(context.Background())
			for {
				refreshDue(context.Background(), time.Now(), busy)
				time.Sleep(tick)
			}
		}()
	})
}

// restore gives opencode back a token it no longer holds, such as after its
// database was replaced. A token about to expire is left to refreshDue.
func restore(ctx context.Context) {
	storeMu.Lock()
	records, err := listLocked()
	storeMu.Unlock()
	if err != nil {
		log.Printf("[entra] list sign-ins: %v", err)
		return
	}
	for _, r := range records {
		if r.Failed != "" || time.Until(r.ExpiresAt) < refreshBefore {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if has, err := signedIn(cctx, r.Server); err == nil && !has {
			if err := hand(cctx, r); err != nil {
				log.Printf("[entra] %s: hand the token to opencode: %v", r.Server, err)
			}
		}
		cancel()
	}
}

// refreshDue refreshes each access token that expires within refreshBefore,
// and hands the new one to opencode. While busy, it waits, until busyLimit.
func refreshDue(ctx context.Context, now time.Time, busy func() bool) {
	storeMu.Lock()
	records, err := listLocked()
	storeMu.Unlock()
	if err != nil {
		log.Printf("[entra] list sign-ins: %v", err)
		return
	}
	for _, r := range records {
		left := r.ExpiresAt.Sub(now)
		if r.Failed != "" || left > refreshBefore {
			continue
		}
		if left > busyLimit && busy != nil && busy() {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := refreshOne(cctx, r.Server, now); err != nil {
			log.Printf("[entra] %s: refresh: %v", r.Server, err)
		}
		cancel()
	}
}

func refreshOne(ctx context.Context, server string, now time.Time) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	r, err := loadLocked(server)
	if err != nil || r == nil || r.Failed != "" {
		return err
	}
	t, err := refresh(ctx, r.app(), r.RefreshToken, r.Scopes)
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Expired {
			r.Failed = e.Error()
			if serr := saveLocked(r); serr != nil {
				return serr
			}
		}
		return err
	}
	r.AccessToken = t.AccessToken
	if t.RefreshToken != "" {
		r.RefreshToken = t.RefreshToken
	}
	r.ExpiresAt = t.expiresAt(now)
	if err := saveLocked(r); err != nil {
		return err
	}
	if err := hand(ctx, r); err != nil {
		return err
	}
	log.Printf("[entra] %s: refreshed, valid until %s", r.Server, r.ExpiresAt.Format(time.RFC3339))
	return nil
}
