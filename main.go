package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/naglezhang/fingersaver/internal/agent"
	"github.com/naglezhang/fingersaver/internal/config"
	"github.com/naglezhang/fingersaver/internal/llm"
	"github.com/naglezhang/fingersaver/internal/tmux"
	"github.com/naglezhang/fingersaver/internal/tui"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	// Start tmux client.
	tc := tmux.NewClient(cfg.TmuxSocketPath)
	if err := tc.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting tmux: %v\n", err)
		os.Exit(1)
	}
	defer tc.Stop()

	// Create LLM provider.
	provider, err := llm.NewProvider(cfg.LLMProvider, cfg.LLMAPIKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating LLM provider: %v\n", err)
		os.Exit(1)
	}

	// Create orchestrator.
	hm := agent.NewHookManager()
	orch := agent.NewOrchestrator(provider, tc, hm, agent.AllTools(tc))
	orch.SetCommandRegistry(agent.NewCommandRegistry(tc))

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

	p := tea.NewProgram(app)

	app.SetSendFn(func(msg tea.Msg) {
		p.Send(msg)
	})

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
		os.Exit(1)
	}
}
