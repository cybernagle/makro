package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/naglezhang/fingersaver/internal/agent/tools"
)

type SlashCommand struct {
	Name        string
	Usage       string
	Description string
	Execute     func(ctx context.Context, args []string) (string, error)
}

type CommandRegistry struct {
	commands map[string]*SlashCommand
	tc       tools.TmuxClient
	guardian tools.Guardian
}

func NewCommandRegistry(tc tools.TmuxClient, guardian tools.Guardian) *CommandRegistry {
	cr := &CommandRegistry{
		commands: make(map[string]*SlashCommand),
		tc:       tc,
		guardian: guardian,
	}
	cr.registerDefaults()
	return cr
}

func (cr *CommandRegistry) registerDefaults() {
	cr.Register(&SlashCommand{
		Name: "create", Usage: "/create <name> [working-dir]",
		Description: "Create a new tmux session",
		Execute: func(ctx context.Context, args []string) (string, error) {
			if len(args) < 1 {
				return "", fmt.Errorf("usage: /create <name> [working-dir]")
			}
			tool := tools.NewCreateSessionTool(cr.tc)
			toolArgs := map[string]any{"name": args[0]}
			if len(args) > 1 {
				toolArgs["working_dir"] = args[1]
			}
			return tool.Execute(ctx, toolArgs)
		},
	})
	cr.Register(&SlashCommand{
		Name: "switch", Usage: "/switch <name>",
		Description: "Switch to a different session",
		Execute: func(ctx context.Context, args []string) (string, error) {
			if len(args) < 1 {
				return "", fmt.Errorf("usage: /switch <name>")
			}
			tool := tools.NewSwitchSessionTool(cr.tc)
			return tool.Execute(ctx, map[string]any{"name": args[0]})
		},
	})
	cr.Register(&SlashCommand{
		Name: "kill", Usage: "/kill <name>",
		Description: "Kill a tmux session",
		Execute: func(ctx context.Context, args []string) (string, error) {
			if len(args) < 1 {
				return "", fmt.Errorf("usage: /kill <name>")
			}
			tool := tools.NewKillSessionTool(cr.tc)
			return tool.Execute(ctx, map[string]any{"name": args[0]})
		},
	})
	cr.Register(&SlashCommand{
		Name: "list", Usage: "/list",
		Description: "List all sessions",
		Execute: func(ctx context.Context, args []string) (string, error) {
			tool := tools.NewListSessionsTool(cr.tc)
			return tool.Execute(ctx, nil)
		},
	})
	cr.Register(&SlashCommand{
		Name: "help", Usage: "/help",
		Description: "Show available commands",
		Execute: func(ctx context.Context, args []string) (string, error) {
			var sb strings.Builder
			sb.WriteString("Available commands:\n")
			for _, cmd := range cr.commands {
				sb.WriteString(fmt.Sprintf("  %-30s %s\n", cmd.Usage, cmd.Description))
			}
			return sb.String(), nil
		},
	})

	cr.Register(&SlashCommand{
		Name:        "watch",
		Usage:       "/watch <session> | /watch stop [session] | /watch list",
		Description: "Start or stop session guardian (auto-approve/reject agent confirmations)",
		Execute: func(ctx context.Context, args []string) (string, error) {
			if cr.guardian == nil {
				return "", fmt.Errorf("guardian not available")
			}
			if len(args) == 0 {
				return "", fmt.Errorf("usage: /watch <session> | /watch stop [session] | /watch list")
			}
			switch args[0] {
			case "stop":
				if len(args) > 1 {
					return "", cr.guardian.Stop(args[1])
				}
				cr.guardian.StopAll()
				return "All guardians stopped.", nil
			case "list":
				active := cr.guardian.ActiveGuardians()
				if len(active) == 0 {
					return "No active guardians.", nil
				}
				return "Watching: " + strings.Join(active, ", "), nil
			default:
				if err := cr.guardian.Watch(ctx, args[0]); err != nil {
					return "", err
				}
				return fmt.Sprintf("Guardian started for %q", args[0]), nil
			}
		},
	})
}

func (cr *CommandRegistry) Register(cmd *SlashCommand) {
	cr.commands[cmd.Name] = cmd
}

func (cr *CommandRegistry) Execute(ctx context.Context, input string) (string, error) {
	cmdName, args, ok := ParseSlashCommand(input)
	if !ok {
		return "", fmt.Errorf("not a slash command")
	}
	cmd, exists := cr.commands[cmdName]
	if !exists {
		return "", fmt.Errorf("unknown command: /%s", cmdName)
	}
	return cmd.Execute(ctx, args)
}

func (cr *CommandRegistry) List() []*SlashCommand {
	result := make([]*SlashCommand, 0, len(cr.commands))
	for _, cmd := range cr.commands {
		result = append(result, cmd)
	}
	return result
}

// ParseSlashCommand parses "/command arg1 arg2" into (command, args, true).
// Returns ("", nil, false) if input doesn't start with /.
func ParseSlashCommand(input string) (string, []string, bool) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") {
		return "", nil, false
	}
	parts := strings.Fields(input[1:])
	if len(parts) == 0 {
		return "", nil, false
	}
	return parts[0], parts[1:], true
}

// ExtractMention extracts @session-name from input and returns
// the session name and the remaining text.
func ExtractMention(input string) (sessionName string, text string) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "@") {
		return "", input
	}
	rest := input[1:]
	name, remaining, found := strings.Cut(rest, " ")
	if !found {
		return name, ""
	}
	return name, strings.TrimSpace(remaining)
}
