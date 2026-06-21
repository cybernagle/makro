package main

import (
	"encoding/json"
	"io"
	"log"
	"os"

	"github.com/naglezhang/makro/internal/notify"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("makro-serve must be run with the 'serve' subcommand (launched by Electron)")
	}

	switch os.Args[1] {
	// Hook forwarders — invoked by Claude Code's Stop/Permission hooks to push
	// an event to the running Makro instance over the socket.
	case "notify":
		if len(os.Args) < 4 {
			log.Fatal("Usage: makro-serve notify <session> <status>")
		}
		session := os.Args[2]
		status := os.Args[3]
		// A "start" status marks the turn as in-progress; everything else is a stop.
		msgType := "agent_stop"
		if status == "start" {
			msgType = "agent_start"
		}
		_ = notify.SendHook(map[string]string{"type": msgType, "session": session, "status": status})
		return
	case "permission":
		if len(os.Args) < 3 {
			log.Fatal("Usage: makro-serve permission <session>")
		}
		_ = notify.SendHook(map[string]string{"type": "permission", "session": os.Args[2]})
		return
	case "capture":
		// Brain capture entry point. Invoked by Claude Code's UserPromptSubmit
		// hook: argv[2] = tmux session name (from the hook command's
		// $(tmux display-message)); stdin = the hook JSON {"prompt","cwd"}.
		// Forward to the running Makro instance; the notifier's OnCapture
		// callback routes it to the brain capture sink → memory-cli.
		// Best-effort: never error into the hook (would slow the agent). If makro
		// isn't running, silently exit.
		session := ""
		if len(os.Args) > 2 {
			session = os.Args[2]
		}
		payload := ""
		if body, rerr := io.ReadAll(os.Stdin); rerr == nil {
			payload = string(body)
		}
		if payload == "" && session == "" {
			return
		}
		_ = notify.SendHook(map[string]string{"type": "capture", "session": session, "payload": payload})
		return
	case "claude-start":
		// Called by Claude Code's SessionStart hook. argv[2] = tmux session name
		// (from the hook command's $(tmux display-message)); stdin = the hook
		// JSON with session_id + transcript_path + cwd. Forward to Makro so the
		// transcript ingester can attribute usage to the tmux session.
		tmuxSession := ""
		if len(os.Args) > 2 {
			tmuxSession = os.Args[2]
		}
		var input struct {
			SessionID      string `json:"session_id"`
			TranscriptPath string `json:"transcript_path"`
			Cwd            string `json:"cwd"`
		}
		if body, rerr := io.ReadAll(os.Stdin); rerr == nil {
			_ = json.Unmarshal(body, &input)
		}
		_ = notify.SendHook(map[string]string{
			"type":              "claude_session_start",
			"session":           tmuxSession,
			"claude_session_id": input.SessionID,
			"transcript_path":   input.TranscriptPath,
			"cwd":               input.Cwd,
		})
		return
	case "serve":
		// continue below
	default:
		log.Fatal("makro-serve must be run with the 'serve' subcommand (launched by Electron)")
	}

	addr := "127.0.0.1:7070"
	var tlsCert, tlsKey, password string
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--addr":
			i++
			if i < len(os.Args) {
				addr = os.Args[i]
			}
		case "--tls-cert":
			i++
			if i < len(os.Args) {
				tlsCert = os.Args[i]
			}
		case "--tls-key":
			i++
			if i < len(os.Args) {
				tlsKey = os.Args[i]
			}
		case "--password":
			i++
			if i < len(os.Args) {
				password = os.Args[i]
			}
		}
	}
	if err := serve(addr, tlsCert, tlsKey, password); err != nil {
		log.Fatal(err)
	}
}
