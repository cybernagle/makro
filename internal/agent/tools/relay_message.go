package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/naglezhang/fingersaver/internal/tmux"
	"github.com/naglezhang/fingersaver/internal/util"
)

func NewRelayMessageTool(tc TmuxClient) Tool {
	return Tool{
		Name:        "relay_message",
		Description: "Relay a structured message between sessions. Follow with wait_until_idle to handle confirmation prompts on the target.",
		Parameters: []Param{
			{Name: "from_session", Type: "string", Description: "Source session name", Required: true},
			{Name: "to_session", Type: "string", Description: "Target session name", Required: true},
			{Name: "message_type", Type: "string", Description: "Message type: review_result, fix_request, context, general", Required: true},
			{Name: "content", Type: "string", Description: "Message body", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			fromSession, _ := args["from_session"].(string)
			toSession, _ := args["to_session"].(string)
			messageType, _ := args["message_type"].(string)
			content, _ := args["content"].(string)

			if fromSession == "" || toSession == "" || messageType == "" || content == "" {
				return "", fmt.Errorf("from_session, to_session, message_type, and content are required")
			}

			srcOutput, err := ReadStructuredOutput(tc, fromSession)
			if err != nil {
				return "", err
			}

			targetRaw, _ := tc.Exec(tmux.CapturePaneCmd(toSession))
			if targetRaw != "" {
				targetOutput := parseStructuredOutput(targetRaw)
				if targetOutput.PendingConfirmation != nil {
					tc.Exec(tmux.SendEscapeCmd(toSession))
					time.Sleep(200 * time.Millisecond)
				}
			}

			msg := formatRelayMessage(fromSession, messageType, content, srcOutput)
			if err := sendText(tc, toSession, msg); err != nil {
				return "", err
			}

			return fmt.Sprintf("Relayed %s from %q to %q", messageType, fromSession, toSession), nil
		},
	}
}

func formatRelayMessage(from, msgType, content string, src *StructuredOutput) string {
	var b strings.Builder
	b.WriteString("═══════════════════════════════════════════\n")
	b.WriteString(fmt.Sprintf("📨 Message from session [%s]\n", from))
	b.WriteString(fmt.Sprintf("Type: %s\n", msgType))
	b.WriteString("═══════════════════════════════════════════\n\n")
	b.WriteString(content)
	b.WriteString("\n\n--- Source Session Summary ---\n")
	b.WriteString(fmt.Sprintf("Status: %s\n", src.Status))
	if src.LastAssistantMessage != "" {
		b.WriteString(fmt.Sprintf("Last Output: %s\n", util.Truncate(src.LastAssistantMessage, 500)))
	}
	if len(src.Errors) > 0 {
		b.WriteString(fmt.Sprintf("Errors: %s\n", strings.Join(src.Errors, "; ")))
	}
	b.WriteString("═══════════════════════════════════════════")
	return b.String()
}
