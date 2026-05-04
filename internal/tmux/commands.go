package tmux

import (
	"fmt"
	"strings"
)

func NewSessionCmd(name, workingDir, shell string) string {
	args := []string{"new-session", "-d", "-s", quoteArg(name)}
	if workingDir != "" {
		args = append(args, "-c", quoteArg(workingDir))
	}
	if shell != "" {
		args = append(args, quoteArg(shell))
	}
	return strings.Join(args, " ")
}

func KillSessionCmd(name string) string {
	return fmt.Sprintf("kill-session -t %s", quoteArg(name))
}

func SwitchClientCmd(sessionName string) string {
	return fmt.Sprintf("switch-client -t %s", quoteArg(sessionName))
}

func SendKeysCmd(sessionName, keys string) string {
	return fmt.Sprintf("send-keys -t %s %s", quoteArg(sessionName), quoteArg(keys))
}

func SendKeysLiteralCmd(sessionName, text string) string {
	return fmt.Sprintf("send-keys -t %s -l %s", quoteArg(sessionName), quoteArg(text))
}

func SendEnterCmd(sessionName string) string {
	return fmt.Sprintf("send-keys -t %s Enter", quoteArg(sessionName))
}

func SendEscapeCmd(sessionName string) string {
	return fmt.Sprintf("send-keys -t %s Escape", quoteArg(sessionName))
}

func SendCJCmd(sessionName string) string {
	return fmt.Sprintf("send-keys -t %s C-j", quoteArg(sessionName))
}

func RenameSessionCmd(oldName, newName string) string {
	return fmt.Sprintf("rename-session -t %s %s", quoteArg(oldName), quoteArg(newName))
}

func ListSessionsCmd() string {
	return "list-sessions"
}

func ListWindowsCmd(sessionName string) string {
	return fmt.Sprintf("list-windows -t %s", quoteArg(sessionName))
}

func CapturePaneCmd(paneID string) string {
	return fmt.Sprintf("capture-pane -t %s -p", quoteArg(paneID))
}

// CapturePaneRangeCmd captures lines from -startFromEnd to -endFromEnd
// (measured from the bottom of the scrollback). -S -N means N lines back from
// the bottom of history; -E -M means M lines back from the cursor.
func CapturePaneRangeCmd(paneID string, startFromEnd, endFromEnd int) string {
	return fmt.Sprintf("capture-pane -t %s -p -S -%d -E -%d", quoteArg(paneID), startFromEnd, endFromEnd)
}

// CapturePaneAllCmd captures the entire scrollback history.
func CapturePaneAllCmd(paneID string) string {
	return fmt.Sprintf("capture-pane -t %s -p -S -", quoteArg(paneID))
}

func SetWindowSizeCmd(sessionName string, width, height int) string {
	return fmt.Sprintf("set-option -t %s window-size %dx%d", quoteArg(sessionName), width, height)
}

func ResizeWindowCmd(sessionName string, width, height int) string {
	return fmt.Sprintf("resize-window -t %s -x %d -y %d", quoteArg(sessionName), width, height)
}

func DetachClientCmd() string {
	return "detach-client"
}

func HasSessionCmd(name string) string {
	return fmt.Sprintf("has-session -t %s", quoteArg(name))
}

func PanePIDCmd(sessionName string) string {
	return fmt.Sprintf("list-panes -t %s -F #{pane_pid}", quoteArg(sessionName))
}

func PaneCurrentCommandCmd(sessionName string) string {
	return fmt.Sprintf("list-panes -t %s -F #{pane_current_command}", quoteArg(sessionName))
}

func quoteArg(s string) string {
	if !strings.ContainsAny(s, " \t'\"\\") {
		return s
	}
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}
