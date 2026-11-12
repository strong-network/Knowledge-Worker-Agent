// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"time"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/chat"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/config"
	"github.com/strong-network/Knowledge-Worker-Agent/internal/skilltokens"
)

// setupSkillTokens lists the agent's skills for Accounts, and gives opencode
// a saved or removed token once no turn is running: opencode 2.x's server
// keeps the environment it started with, and restarting it ends a turn.
func setupSkillTokens() {
	skilltokens.SetSkills(func(ctx context.Context) ([]skilltokens.Skill, error) {
		raw, err := chat.SkillFiles(ctx, config.Workspace)
		if err != nil {
			return nil, err
		}
		out := make([]skilltokens.Skill, 0, len(raw))
		for _, s := range raw {
			out = append(out, skilltokens.Skill{Name: s.Name, Path: s.Location})
		}
		return out, nil
	})
	changed := make(chan struct{}, 1)
	skilltokens.SetOnChange(func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	go func() {
		for range changed {
			for chat.HasActiveTasks() {
				time.Sleep(time.Second)
			}
			chat.ReloadServerAgents()
		}
	}()
}
