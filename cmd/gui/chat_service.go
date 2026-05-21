package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/naglezhang/fingersaver/internal/agent"
	"github.com/naglezhang/fingersaver/internal/agent/tools"
	"github.com/naglezhang/fingersaver/internal/config"
	"github.com/naglezhang/fingersaver/internal/llm"
	"github.com/naglezhang/fingersaver/internal/tmux"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type ChatService struct {
	app  *application.App
	orch *agent.Orchestrator
	tc   *tmux.Client
}

func NewChatService(app *application.App) *ChatService {
	return &ChatService{app: app}
}

func (s *ChatService) SetApp(app *application.App) {
	s.app = app
}

func (s *ChatService) Startup(ctx application.Context) {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("[chat_service] config load error: %v", err)
		return
	}

	provider, err := llm.NewProvider(cfg.LLMProvider, cfg.LLMAPIKey, cfg.LLMBaseURL)
	if err != nil {
		log.Printf("[chat_service] provider error: %v", err)
		return
	}

	// Connect to tmux (detect socket like main.go does)
	socketPath := detectTmuxSocket()
	tc := tmux.NewClient(socketPath, false)
	if err := tc.Start(context.Background()); err != nil {
		log.Printf("[chat_service] tmux start error: %v", err)
		return
	}

	hm := agent.NewHookManager()
	assessor := agent.NewSessionAssessor(provider, cfg.LLMModel, cfg.GuardianPrompt)
	cwd, _ := os.Getwd()
	notifier := agent.NewAgentNotifier()

	orch := agent.NewOrchestrator(provider, tc, hm, tools.AllTools(tc, assessor, cwd, notifier))
	orch.SetCommandRegistry(agent.NewCommandRegistry(tc))
	homeDir, _ := os.UserHomeDir()
	skillDirs := []string{
		filepath.Join(homeDir, ".fingersaver", "skills"),
		filepath.Join(".", ".fingersaver", "skills"),
	}
	orch.LoadSkills(skillDirs)
	orch.SetModel(cfg.LLMModel)
	orch.SetMaxContextMessages(cfg.MaxContextMessages)
	orch.SetSystemPrompt(agent.DefaultSystemPrompt())

	notifier.OnSession(func(session, content string) error {
		return tools.DirectSend(tc, session, content)
	})
	go notifier.Start(context.Background())

	s.orch = orch
	s.tc = tc
	log.Printf("[chat_service] initialized provider=%s model=%s", cfg.LLMProvider, cfg.LLMModel)
}

func (s *ChatService) SendMessage(input string) error {
	if s.orch == nil {
		s.emit("chat:error", "Orchestrator not initialized (check config/API key)")
		return nil
	}

	go func() {
		ctx := context.Background()
		ch, err := s.orch.ProcessInput(ctx, input)
		if err != nil {
			s.emit("chat:error", err.Error())
			return
		}

		for ev := range ch {
			switch ev.Type {
			case agent.EventText:
				s.emit("chat:text", ev.Content)
			case agent.EventToolCall:
				data, _ := json.Marshal(map[string]any{
					"tool": ev.ToolName,
					"args": ev.ToolArgs,
				})
				s.emit("chat:tool_call", string(data))
			case agent.EventToolResult:
				data, _ := json.Marshal(map[string]any{
					"tool":   ev.ToolName,
					"result": ev.ToolResult,
				})
				s.emit("chat:tool_result", string(data))
			case agent.EventDone:
				s.emit("chat:done", "")
			}
		}
	}()
	return nil
}

func (s *ChatService) Cancel() {
	if s.orch != nil {
		s.orch.Cancel()
	}
}

func (s *ChatService) emit(event string, data string) {
	if s.app != nil {
		s.app.Event.Emit(event, data)
	}
}

func detectTmuxSocket() string {
	home, _ := os.UserHomeDir()
	sock := filepath.Join(home, ".fingersaver", "tmux.sock")
	if _, err := os.Stat(sock); err == nil {
		return sock
	}
	return ""
}
