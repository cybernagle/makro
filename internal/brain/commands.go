package brain

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/naglezhang/makro/internal/agent"
)

// RegisterCommands wires the brain's slash commands into a CommandRegistry.
// Call this from main.go / chat_service.go after the brain is built, so the
// commands reach the chat pane's command suggestions and Execute path.
//
// Commands:
//
//	/brain wake          — trigger one immediate wake (async; result pushed later)
//	/inbox               — list open proposals
//	/inbox accept <id> [reason]
//	/inbox reject <id> [reason]
//
// The registry keys all commands under "brain" and "inbox"; subcommands (accept/
// reject) are parsed inside the inbox Execute from args[0].
func RegisterCommands(cr *agent.CommandRegistry, b *Brain) {
	if cr == nil || b == nil {
		return
	}

	cr.Register(&agent.SlashCommand{
		Name:        "brain",
		Usage:       "/brain wake",
		Description: "Trigger the brain's wake cycle immediately (proposal pushed when ready)",
		Execute: func(ctx context.Context, args []string) (string, error) {
			if len(args) == 0 || args[0] != "wake" {
				return "用法：/brain wake（立即触发一轮 brain wake）", nil
			}
			b.WakeNow(ctx)
			return "🧠 已触发 brain wake，结果稍后推送（LLM 调用可能要 10-60s）。", nil
		},
	})

	cr.Register(&agent.SlashCommand{
		Name:        "inbox",
		Usage:       "/inbox | /inbox accept <id> [reason] | /inbox reject <id> [reason]",
		Description: "List brain proposals, or accept/reject one by ID",
		Execute: func(ctx context.Context, args []string) (string, error) {
			return handleInbox(ctx, b, args)
		},
	})
}

// handleInbox dispatches the inbox subcommand. args[0] is the subcommand
// (accept/reject), or empty for a bare /inbox (list).
func handleInbox(ctx context.Context, b *Brain, args []string) (string, error) {
	// Bare /inbox → list open proposals.
	if len(args) == 0 {
		return listInbox(ctx, b)
	}

	switch args[0] {
	case "accept", "reject":
		if len(args) < 2 {
			return fmt.Sprintf("用法：/inbox %s <id> [原因]", args[0]), nil
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Sprintf("无效的 proposal id：%q（要是数字）", args[1]), nil
		}
		reason := ""
		if len(args) > 2 {
			reason = strings.Join(args[2:], " ")
		}
		verdict := VerdictAccept
		if args[0] == "reject" {
			verdict = VerdictReject
		}
		return b.Feedback().Apply(ctx, id, verdict, reason)
	default:
		return "用法：/inbox（列表）| /inbox accept <id> [原因] | /inbox reject <id> [原因]", nil
	}
}

// listInbox renders the open-proposals list for the chat reply.
func listInbox(ctx context.Context, b *Brain) (string, error) {
	open, err := b.Inbox().ListOpen(ctx)
	if err != nil {
		return "读 inbox 失败：" + err.Error(), nil
	}
	if len(open) == 0 {
		return "📭 inbox 里没有 open proposal。用 /brain wake 触发一轮。", nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📬 inbox（%d 个 open proposal）：\n", len(open)))
	for _, p := range open {
		age := time.Since(p.CreatedAt).Round(time.Hour)
		fmt.Fprintf(&sb, "  [#%d] %s（confidence %.0f%%, %s, %s 前）\n",
			p.ID, p.Title, p.Confidence*100, p.Domain, age)
	}
	sb.WriteString("\n/inbox accept <id> 或 /inbox reject <id> [原因]")
	return sb.String(), nil
}
