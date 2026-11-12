// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodev2

import (
	"context"
	"os/exec"
	"strings"
)

// Cmd returns opencode with args as the installed version needs it: 1.x runs
// them as given, 2.x through Command.
func Cmd(ctx context.Context, bin string, args ...string) *exec.Cmd {
	if Major(bin) < 2 {
		return exec.CommandContext(ctx, bin, args...)
	}
	return Command(ctx, bin, args...)
}

// Command returns opencode with args, sent to KWA's server. When the server
// can't start, the command runs its own private one instead (--standalone).
//
// The message of a `run` (the one argument after "--") goes on stdin instead,
// which 2.x reads as it is. 2.2.5 rebuilds the message from its arguments: it
// wraps any argument with a space in quotes, escaping the quotes inside, and
// counts the arguments after "--" twice. A caller that sets Stdin itself
// replaces the message.
func Command(ctx context.Context, bin string, args ...string) *exec.Cmd {
	env := append(processEnv(), "OPENCODE_DISABLE_AUTOUPDATE=1")
	flags := []string{"--standalone"}
	if url, password, err := Ensure(ctx); err == nil {
		flags = []string{"--server", url}
		env = append(env, "OPENCODE_PASSWORD="+password)
	} else {
		env = configEnv(env)
	}
	args, message, hasMessage := runMessage(args)
	cmd := exec.CommandContext(ctx, bin, withFlags(args, flags)...)
	cmd.Env = env
	if hasMessage {
		cmd.Stdin = strings.NewReader(message)
	}
	return cmd
}

// runMessage takes the message off a `run` that ends in "--" and one
// argument, and returns the arguments without them.
func runMessage(args []string) (rest []string, message string, ok bool) {
	n := len(args)
	if n < 3 || args[0] != "run" || args[n-2] != "--" {
		return args, "", false
	}
	return args[:n-2], args[n-1], true
}

// withFlags puts flags before a "--" that ends the options, or at the end.
func withFlags(args, flags []string) []string {
	out := make([]string, 0, len(args)+len(flags))
	for i, a := range args {
		if a == "--" {
			out = append(out, flags...)
			return append(out, args[i:]...)
		}
		out = append(out, a)
	}
	return append(out, flags...)
}
