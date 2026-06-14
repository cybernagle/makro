package main

import (
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
