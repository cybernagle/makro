package agent

import (
	"context"
	"fmt"
	"strings"
)

type SlashCommand struct {
	Name        string
	Usage       string
	Description string
	Execute     func(ctx context.Context, args []string) (string, error)
}

type CommandRegistry struct {
	commands map[string]*SlashCommand
	tc       TmuxClient
}

func NewCommandRegistry(tc TmuxClient) *CommandRegistry {
	cr := &CommandRegistry{
		commands: make(map[string]*SlashCommand),
		tc:       tc,
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
			tool := NewCreateSessionTool(cr.tc)
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
			tool := NewSwitchSessionTool(cr.tc)
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
			tool := NewKillSessionTool(cr.tc)
			return tool.Execute(ctx, map[string]any{"name": args[0]})
		},
	})
	cr.Register(&SlashCommand{
		Name: "list", Usage: "/list",
		Description: "List all sessions",
		Execute: func(ctx context.Context, args []string) (string, error) {
			tool := NewListSessionsTool(cr.tc)
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
