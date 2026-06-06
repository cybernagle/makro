package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/naglezhang/makro/internal/agent"
	"github.com/naglezhang/makro/internal/agent/tools"
	"github.com/naglezhang/makro/internal/config"
	"github.com/naglezhang/makro/internal/llm"
	"github.com/naglezhang/makro/internal/tmux"
	"github.com/naglezhang/makro/internal/tui"
)

var (
	showHelp    = flag.Bool("help", false, "Show help")
	showVersion = flag.Bool("version", false, "Show version")
	showConfig  = flag.Bool("config", false, "Show current configuration and exit")
	chatMode    = flag.Bool("chat", false, "CLI chat mode (no TUI, for testing)")
	phoneLayout = flag.Bool("phone", false, "Use phone layout (vertical split)")
)

const version = "0.4.19"

func main() {
	// Handle subcommands that communicate with a running Makro instance.
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "notify":
			runSocketCommand("agent_stop")
			return
		case "chat":
			runSocketCommand("chat")
			return
		case "send":
			runSocketCommand("session")
			return
		case "permission":
			runSocketCommand("permission")
			return
		}
	}

	flag.BoolVar(showHelp, "h", false, "Show help")
	flag.Parse()

	if *showHelp {
		fmt.Print(helpText())
		return
	}
	if *showVersion {
		fmt.Printf("makro %s\n", version)
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
	log.Printf("[main] makro %s starting provider=%s model=%s chat=%v", version, cfg.LLMProvider, cfg.LLMModel, *chatMode)

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

	// Create assessor and orchestrator.
	hm := agent.NewHookManager()
	assessor := agent.NewSessionAssessor(provider, cfg.LLMModel, cfg.GuardianPrompt)
	cwd, _ := os.Getwd()

	// Create hook notifier for agent stop notifications.
	notifier := agent.NewAgentNotifier()

	orch := agent.NewOrchestrator(provider, tc, hm, tools.AllTools(tc, assessor, cwd, notifier))
	orch.SetCommandRegistry(agent.NewCommandRegistry(tc))
	homeDir, _ := os.UserHomeDir()
	skillDirs := []string{
		filepath.Join(homeDir, ".makro", "skills"),
		filepath.Join(".", ".makro", "skills"),
	}
	if err := orch.LoadSkills(skillDirs); err != nil {
		log.Printf("[main] warning: could not load skills: %v", err)
	}
	orch.SetModel(cfg.LLMModel)
	orch.SetMaxContextMessages(cfg.MaxContextMessages)
	orch.SetSystemPrompt(agent.DefaultSystemPrompt())

	// Register callbacks BEFORE Start to avoid race condition.
	// These must be set before both --chat and TUI paths.
	notifier.OnSession(func(session, content string) error {
		return tools.DirectSend(tc, session, content)
	})

	if err := notifier.Start(ctx); err != nil {
		log.Printf("[main] warning: hook notifier failed to start: %v", err)
		notifier = nil
	}
	if notifier != nil {
		defer notifier.Stop()

		// Auto-configure Claude Code stop hook.
		if executablePath, err := os.Executable(); err != nil {
			log.Printf("[main] warning: could not resolve executable path for Claude hooks: %v", err)
		} else {
			if err := agent.EnsureStopHook(cfg.ClaudeDir, executablePath); err != nil {
				log.Printf("[main] warning: could not configure Claude stop hook: %v", err)
			}
			if err := agent.EnsurePermissionHook(cfg.ClaudeDir, executablePath); err != nil {
				log.Printf("[main] warning: could not configure Claude permission hook: %v", err)
			}
		}
	}

	if *chatMode {
		runChat(ctx, orch)
		return
	}

	// Create and run TUI.
	app := tui.NewAppModel(orch, tc, notifier, assessor)
	if *phoneLayout {
		app.SetLayout(tui.LayoutPhone)
	}

	// Register TUI-specific chat callback after app is created.
	if notifier != nil {
		notifier.OnChat(func(role, content string) {
			app.SendChatMessage(role, content)
		})

		notifier.OnAgentStop(func(session, status string) {
			s := status
			if s == "" {
				s = "stopped"
			}
			msg := fmt.Sprintf("Session %s %s", session, s)
			if out, err := tools.ReadStructuredOutput(tc, session); err == nil && out.LastAssistantMessage != "" {
				msg += "\n" + out.LastAssistantMessage
			}
			app.SendChatMessage("system", msg)
		})

		notifier.OnPermission(func(session string) {
			app.SendChatMessage("system", fmt.Sprintf("Session %s waiting for permission", session))
		})
	}

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
// Returns the socket path and whether Makro owns the server.
func resolveTmuxServer(cfg *config.Config, chatMode bool) (string, bool, error) {
	switch cfg.TmuxMode {
	case config.TmuxModeDedicated:
		return cfg.TmuxSocketPath, true, nil
	case config.TmuxModeShared:
		info := tmux.DetectServer()
		if info == nil {
			return "", false, fmt.Errorf("tmux_mode=shared but no running tmux server found")
		}
		log.Printf("[main] using shared tmux server: %s", info.SocketPath)
		return info.SocketPath, false, nil
	case config.TmuxModeAuto:
		info := tmux.DetectServer()
		if info == nil {
			log.Printf("[main] no existing tmux server, creating dedicated one")
			return cfg.TmuxSocketPath, true, nil
		}
		if isTerminal(os.Stdin) && !chatMode {
			fmt.Fprintf(os.Stderr, "Found tmux server (%s, %d sessions). Use it? [Y/n]: ",
				info.SocketPath, len(info.Sessions))
			reader := bufio.NewReader(os.Stdin)
			line, err := reader.ReadString('\n')
			if err != nil {
				log.Printf("[main] stdin read error (%v), using detected server", err)
				cfg.TmuxMode = config.TmuxModeShared
				saveConfig(cfg)
				return info.SocketPath, false, nil
			}
			line = strings.TrimSpace(strings.ToLower(line))
			if line == "" || line == "y" || line == "yes" {
				log.Printf("[main] using existing tmux server: %s", info.SocketPath)
				cfg.TmuxMode = config.TmuxModeShared
				saveConfig(cfg)
				return info.SocketPath, false, nil
			}
		} else {
			log.Printf("[main] auto-detected tmux server: %s", info.SocketPath)
			return info.SocketPath, false, nil
		}
		log.Printf("[main] user chose dedicated server")
		cfg.TmuxMode = config.TmuxModeDedicated
		saveConfig(cfg)
		return cfg.TmuxSocketPath, true, nil
	default:
		return cfg.TmuxSocketPath, true, nil
	}
}

func saveConfig(cfg *config.Config) {
	if err := cfg.Save(); err != nil {
		log.Printf("[main] warning: could not save config: %v", err)
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
	fmt.Println("Makro CLI Chat (type 'exit' to quit)")
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
	return `makro - AI coding agent orchestrator

USAGE
  makro [flags]
  makro <subcommand> [args]

FLAGS
  -h, --help      Show help
  --version       Show version
  --config        Show current configuration and exit
  --chat          CLI chat mode (no TUI, for e2e testing)
  --phone         Use phone layout (vertical split)

SUBCOMMANDS
  notify <session> <status>  Send agent stop notification (used by Claude Code Stop hook)
  chat <role> <content>      Send message to Makro chat window
  send <session> <message>   Send message to a tmux session

CONFIGURATION
  Makro reads from Claude settings (claude_dir/settings.json):
  - ANTHROPIC_AUTH_TOKEN  -> API key
  - ANTHROPIC_BASE_URL   -> Custom API endpoint
  - ANTHROPIC_DEFAULT_SONNET_MODEL -> Model name

  Override with environment variables:
  - MAKRO_LLM_PROVIDER  (anthropic|openai)
  - MAKRO_LLM_API_KEY
  - MAKRO_LLM_MODEL
  - ANTHROPIC_API_KEY / OPENAI_API_KEY

  Or create ~/.makro/config.json for persistent settings.

KEY BINDINGS
  Ctrl+O        Switch between Chat and Viewer panes
  Ctrl+D        Quit immediately
  Ctrl+R        Force layout recalculation
  [ / ]         Switch between tmux sessions (in Viewer)
  Up/Down       Navigate input history (in Chat)
  Enter         Send message
  Ctrl+C        Clear sticky target (press twice to quit)
  Ctrl+A / Ctrl+E  Jump to start/end of input line

CHAT COMMANDS
  @session text   Send text to a tmux session
  /help           Show available slash commands
  /layout phone   Switch to vertical (phone) layout
  /layout default Switch to horizontal (default) layout
`
}

// runSocketCommand sends a typed message to the Makro Unix socket.
// Subcommands: notify (agent_stop), chat (chat), send (session).
func runSocketCommand(msgType string) {
	payload := buildSocketPayload(msgType)
	if payload == nil {
		os.Exit(1)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	sockPath := filepath.Join(home, ".makro", "hooks.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
	if err != nil {
		// Silently exit — Makro may not be running.
		os.Exit(0)
	}
	defer conn.Close()

	msg, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: marshal: %v\n", err)
		os.Exit(1)
	}
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		fmt.Fprintf(os.Stderr, "error: deadline: %v\n", err)
		os.Exit(1)
	}
	if _, err := conn.Write(msg); err != nil {
		fmt.Fprintf(os.Stderr, "error: write: %v\n", err)
		os.Exit(1)
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}

	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		os.Exit(0)
	}
	resp := strings.TrimSpace(string(buf[:n]))
	if strings.HasPrefix(resp, "error:") {
		fmt.Fprintf(os.Stderr, "%s\n", resp)
		os.Exit(1)
	}
}

func buildSocketPayload(msgType string) map[string]string {
	switch msgType {
	case "agent_stop":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "Usage: makro notify <session> <status>\n")
			return nil
		}
		return map[string]string{
			"type":    "agent_stop",
			"session": os.Args[2],
			"status":  os.Args[3],
		}
	case "chat":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "Usage: makro chat <role> <content>\n")
			return nil
		}
		return map[string]string{
			"type":    "chat",
			"role":    os.Args[2],
			"content": strings.Join(os.Args[3:], " "),
		}
	case "session":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "Usage: makro send <session> <message>\n")
			return nil
		}
		return map[string]string{
			"type":    "session",
			"session": os.Args[2],
			"content": strings.Join(os.Args[3:], " "),
		}
	case "permission":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: makro permission <session>\n")
			return nil
		}
		return map[string]string{
			"type":    "permission",
			"session": os.Args[2],
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", msgType)
		return nil
	}
}
