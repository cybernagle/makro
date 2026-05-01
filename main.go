package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/naglezhang/fingersaver/internal/agent"
	"github.com/naglezhang/fingersaver/internal/config"
	"github.com/naglezhang/fingersaver/internal/llm"
	"github.com/naglezhang/fingersaver/internal/tmux"
	"github.com/naglezhang/fingersaver/internal/tui"
)

var (
	showHelp    = flag.Bool("help", false, "Show help")
	showVersion = flag.Bool("version", false, "Show version")
	showConfig  = flag.Bool("config", false, "Show current configuration and exit")
	chatMode    = flag.Bool("chat", false, "CLI chat mode (no TUI, for testing)")
)

const version = "0.1.0"

func main() {
	flag.BoolVar(showHelp, "h", false, "Show help")
	flag.Parse()

	if *showHelp {
		fmt.Print(helpText())
		return
	}
	if *showVersion {
		fmt.Printf("fingersaver %s\n", version)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Discard log output early to prevent any log writes to stderr
	// (including from config.Load) from corrupting the Bubbletea TUI.
	log.SetOutput(io.Discard)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		cancel()
	}()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// Set up debug log to file; discard log output if file creation fails
	// to prevent log writes to stderr from corrupting the Bubbletea TUI.
	if err := os.MkdirAll(cfg.DataDir, 0o755); err == nil {
		logFile, err := os.Create(filepath.Join(cfg.DataDir, "debug.log"))
		if err == nil {
			log.SetOutput(logFile)
			log.SetFlags(log.Ltime | log.Lmicroseconds)
			defer logFile.Close()
		}
	}
	log.Printf("[main] fingersaver %s starting provider=%s model=%s chat=%v", version, cfg.LLMProvider, cfg.LLMModel, *chatMode)

	if *showConfig {
		fmt.Print(cfg.Summary())
		return
	}

	if err := cfg.ValidateAPIKey(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Resolve tmux server: detect, prompt, or create dedicated.
	socketPath, owned, err := resolveTmuxServer(cfg, *chatMode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	tc := tmux.NewClient(socketPath, owned)
	if err := tc.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting tmux: %v\n", err)
		os.Exit(1)
	}
	defer tc.Stop()

	// Create LLM provider.
	provider, err := llm.NewProvider(cfg.LLMProvider, cfg.LLMAPIKey, cfg.LLMBaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating LLM provider: %v\n", err)
		os.Exit(1)
	}

	// Create orchestrator.
	hm := agent.NewHookManager()
	orch := agent.NewOrchestrator(provider, tc, hm, agent.AllTools(tc))
	orch.SetCommandRegistry(agent.NewCommandRegistry(tc))
	orch.SetModel(cfg.LLMModel)

	if *chatMode {
		runChat(ctx, orch)
		return
	}

	// Create and run TUI.
	app := tui.NewAppModel(orch, tc)

	// Set up chat history persistence.
	if cfg.ChatHistoryPath != "" {
		history, err := tui.NewChatHistory(cfg.ChatHistoryPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not open chat history: %v\n", err)
		} else {
			defer history.Close()
			app.SetChatHistory(history)
		}
	}

	// Set sendFn before NewProgram — the closure captures programSend by
	// reference so the copy inside Bubbletea will see the real send fn.
	var programSend func(tea.Msg)
	app.SetSendFn(func(msg tea.Msg) { programSend(msg) })

	p := tea.NewProgram(app)
	programSend = func(msg tea.Msg) { p.Send(msg) }

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
		os.Exit(1)
	}
}

// resolveTmuxServer determines which tmux server to use based on config.
// Returns the socket path and whether FingerSaver owns the server.
func resolveTmuxServer(cfg *config.Config, chatMode bool) (string, bool, error) {
	switch cfg.TmuxMode {
	case "dedicated":
		return cfg.TmuxSocketPath, true, nil
	case "shared":
		info := tmux.DetectServer()
		if info == nil {
			return "", false, fmt.Errorf("tmux_mode=shared but no running tmux server found")
		}
		log.Printf("[main] using shared tmux server: %s", info.SocketPath)
		return info.SocketPath, false, nil
	case "auto":
		info := tmux.DetectServer()
		if info == nil {
			log.Printf("[main] no existing tmux server, creating dedicated one")
			return cfg.TmuxSocketPath, true, nil
		}
		// Interactive prompt if stdin is a terminal and not in chat mode.
		if isTerminal(os.Stdin) && !chatMode {
			fmt.Fprintf(os.Stderr, "Found tmux server (%s, %d sessions). Use it? [Y/n]: ",
				info.SocketPath, len(info.Sessions))
			reader := bufio.NewReader(os.Stdin)
			line, err := reader.ReadString('\n')
			if err != nil {
				// EOF or I/O error — default to using detected server.
				log.Printf("[main] stdin read error (%v), using detected server", err)
				return info.SocketPath, false, nil
			}
			line = strings.TrimSpace(strings.ToLower(line))
			if line == "" || line == "y" || line == "yes" {
				log.Printf("[main] using existing tmux server: %s", info.SocketPath)
				return info.SocketPath, false, nil
			}
		} else {
			// Non-interactive: auto-use the detected server.
			log.Printf("[main] auto-detected tmux server: %s", info.SocketPath)
			return info.SocketPath, false, nil
		}
		log.Printf("[main] user chose dedicated server")
		return cfg.TmuxSocketPath, true, nil
	default:
		return cfg.TmuxSocketPath, true, nil
	}
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// runChat runs a simple CLI chat loop for e2e testing without TUI.
func runChat(ctx context.Context, orch *agent.Orchestrator) {
	fmt.Println("FingerSaver CLI Chat (type 'exit' to quit)")
	fmt.Println()
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB buffer for long prompts

	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "exit" || input == "quit" {
			fmt.Println("Bye.")
			return
		}

		events, err := orch.ProcessInput(ctx, input)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		for e := range events {
			switch e.Type {
			case agent.EventText:
				fmt.Print(e.Content)
			case agent.EventToolCall:
				fmt.Printf("\n[Calling %s]\n", e.ToolName)
			case agent.EventToolResult:
				fmt.Printf("\n[%s done]\n", e.ToolName)
			case agent.EventDone:
				fmt.Println()
			}
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
	}
}

func helpText() string {
	return `fingersaver - AI coding agent orchestrator

USAGE
  fingersaver [flags]

FLAGS
  -h, --help      Show help
  --version       Show version
  --config        Show current configuration and exit
  --chat          CLI chat mode (no TUI, for e2e testing)

CONFIGURATION
  FingerSaver reads from Claude settings (claude_dir/settings.json):
  - ANTHROPIC_AUTH_TOKEN  -> API key
  - ANTHROPIC_BASE_URL   -> Custom API endpoint
  - ANTHROPIC_DEFAULT_SONNET_MODEL -> Model name

  Override with environment variables:
  - FINGERSAVER_LLM_PROVIDER  (anthropic|openai)
  - FINGERSAVER_LLM_API_KEY
  - FINGERSAVER_LLM_MODEL
  - ANTHROPIC_API_KEY / OPENAI_API_KEY

  Or create ~/.fingersaver/config.json for persistent settings.

KEY BINDINGS
  Tab           Switch between Chat and Viewer panes
  [ / ]         Switch between tmux sessions (in Viewer)
  Up/Down       Navigate input history (in Chat)
  Enter         Send message
  Ctrl+C        Exit

CHAT COMMANDS
  @session text   Send text to a tmux session
  /help           Show available slash commands
`
}
