// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
)

// The test binary doubles as a fake whisper-server (VOICE_TEST_ROLE=engine) and
// as a fake Knowledge Worker Agent that owns one (VOICE_TEST_ROLE=parent), so the engine's
// process handling is tested without the real engine.
func TestMain(m *testing.M) {
	switch os.Getenv("VOICE_TEST_ROLE") {
	case "engine":
		os.Exit(fakeEngineMain(os.Args[1:]))
	case "parent":
		os.Exit(fakeParentMain())
	}
	os.Exit(m.Run())
}

func fakeEngineMode() string {
	if f := os.Getenv("VOICE_TEST_MODEFILE"); f != "" {
		b, _ := os.ReadFile(f)
		return strings.TrimSpace(string(b))
	}
	return os.Getenv("VOICE_TEST_MODE")
}

func record(v map[string]any) {
	path := os.Getenv("VOICE_TEST_LOG")
	if path == "" {
		return
	}
	b, _ := json.Marshal(v)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

func fakeEngineMain(args []string) int {
	mode := fakeEngineMode()
	if len(args) == 1 && args[0] == "--help" {
		if mode == "no-help" {
			fmt.Fprintln(os.Stderr, "error: cannot load backend")
			return 1
		}
		return 0
	}
	cwd, _ := os.Getwd()
	entries, _ := os.ReadDir(cwd)
	record(map[string]any{"event": "start", "pid": os.Getpid(), "args": args, "cwd": cwd,
		"cwd_entries": len(entries), "env": os.Environ()})
	if mode == "fail-start" {
		return 1
	}
	// Like the real engine once it is stuck.
	signal.Ignore(syscall.SIGTERM)
	port := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--port" {
			port = args[i+1]
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /inference", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b, _ := io.ReadAll(f)
		entries, _ := os.ReadDir(cwd)
		record(map[string]any{"event": "request", "pid": os.Getpid(), "bytes": len(b), "riff": string(b[:min(4, len(b))]),
			"prompt": r.FormValue("prompt"), "audio_ctx": r.FormValue("audio_ctx"),
			"temperature_inc": r.FormValue("temperature_inc"), "response_format": r.FormValue("response_format"),
			"cwd_entries": len(entries)})
		switch mode {
		case "hang":
			select {}
		case "crash":
			os.Exit(3)
		case "error":
			w.WriteHeader(500)
			json.NewEncoder(w).Encode(map[string]string{"error": "boom"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"text": fmt.Sprintf(" heard %d samples", (len(b)-44)/2)})
	})
	if err := http.ListenAndServe("127.0.0.1:"+port, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

// fakeParentMain starts an engine, prints its pid and waits to be killed.
func fakeParentMain() int {
	e := newEngine(os.Args[0], os.Getenv("VOICE_TEST_MODEL"))
	e.extraEnv = []string{"VOICE_TEST_ROLE=engine", "VOICE_TEST_MODE=ok"}
	e.Warm()
	fmt.Println(e.running())
	select {}
}
