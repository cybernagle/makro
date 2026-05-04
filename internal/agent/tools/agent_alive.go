package tools

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/naglezhang/fingersaver/internal/tmux"
)

// knownAgents are the coding agent binary names we recognize.
var knownAgents = map[string]bool{
	"claude":             true,
	"copilot":            true,
	"github-copilot-cli": true,
	"codex":              true,
}

// knownShells are processes that indicate the agent has exited.
var knownShells = map[string]bool{
	"bash": true, "zsh": true, "sh": true, "fish": true, "dash": true,
}

// AgentStatus describes the result of an agent alive check.
type AgentStatus struct {
	Alive  bool
	Agent  string // "claude", "copilot", "codex", or ""
	Reason string // human-readable explanation
}

// checkAgentAlive uses approach C: pane_current_command first, then process tree.
func checkAgentAlive(tc TmuxClient, sessionName string) AgentStatus {
	// Step 1: Check pane_current_command.
	cmd, err := tc.Exec(tmux.PaneCurrentCommandCmd(sessionName))
	if err != nil {
		return AgentStatus{Alive: false, Reason: fmt.Sprintf("session %q not found", sessionName)}
	}
	cmd = strings.TrimSpace(cmd)

	// Direct match: agent is the foreground process or a direct wrapper around it.
	if agent, found := knownAgentFromCommand(cmd); found {
		return AgentStatus{Alive: true, Agent: agent, Reason: "agent is running"}
	}

	// If it's a shell, agent might be dead or running a subprocess.
	cmdBase := filepath.Base(cmd)
	if knownShells[cmdBase] {
		return checkProcessTree(tc, sessionName)
	}

	// Unknown process — could be agent running a tool (go, node, python, etc.)
	// Check process tree to be sure.
	return checkProcessTree(tc, sessionName)
}

// checkProcessTree walks the process tree under the pane's PID.
func checkProcessTree(tc TmuxClient, sessionName string) AgentStatus {
	pidStr, err := tc.Exec(tmux.PanePIDCmd(sessionName))
	if err != nil {
		return AgentStatus{Alive: false, Reason: fmt.Sprintf("cannot get pane PID for %q", sessionName)}
	}
	pidStr = strings.TrimSpace(pidStr)
	if pidStr == "" {
		return AgentStatus{Alive: false, Reason: "empty pane PID"}
	}

	// Build process tree from ps output.
	tree, err := buildProcessTree()
	if err != nil {
		// Can't check process tree — fail open.
		return AgentStatus{Alive: true, Reason: "cannot verify agent status, allowing send"}
	}

	// Walk children of pane PID, looking for known agents.
	if agent, found := findAgentInTree(tree, pidStr); found {
		return AgentStatus{Alive: true, Agent: agent, Reason: "agent found in process tree"}
	}

	return AgentStatus{Alive: false, Reason: "agent process not found, only shell running"}
}

// procEntry is a simplified process entry.
type procEntry struct {
	pid     string
	ppid    string
	cmd     string
	command string
}

// buildProcessTree parses ps output into a parent -> children map.
func buildProcessTree() (map[string][]procEntry, error) {
	out, err := exec.Command("ps", "-o", "pid=,ppid=,command=", "-ax").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}

	children := make(map[string][]procEntry)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, ppid := fields[0], fields[1]
		command := strings.Join(fields[2:], " ")
		cmd := fields[2]
		children[ppid] = append(children[ppid], procEntry{
			pid:     pid,
			ppid:    ppid,
			cmd:     filepath.Base(cmd),
			command: command,
		})
	}
	return children, nil
}

// findAgentInTree recursively walks the process tree from rootPID
// looking for any process matching a known agent executable chain.
func findAgentInTree(tree map[string][]procEntry, rootPID string) (string, bool) {
	var walk func(pid string) (string, bool)
	walk = func(pid string) (string, bool) {
		for _, child := range tree[pid] {
			if agent, found := knownAgentFromCommand(child.command); found {
				return agent, true
			}
			if agent, found := knownAgentFromCommand(child.cmd); found {
				return agent, true
			}
			if agent, found := walk(child.pid); found {
				return agent, found
			}
		}
		return "", false
	}
	return walk(rootPID)
}

func knownAgentFromCommand(command string) (string, bool) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", false
	}
	return knownAgentFromArgs(strings.Fields(command))
}

func knownAgentFromArgs(args []string) (string, bool) {
	if len(args) == 0 {
		return "", false
	}

	token := normalizeProcessToken(args[0])
	if agent, found := knownAgentFromExecutable(token); found {
		return agent, true
	}

	switch executableName(token) {
	case "env":
		i := 1
		for i < len(args) && isEnvAssignment(args[i]) {
			i++
		}
		return knownAgentFromArgs(args[i:])
	case "nohup":
		return knownAgentFromArgs(args[1:])
	case "timeout", "gtimeout":
		i := 1
		for i < len(args) && strings.HasPrefix(args[i], "-") {
			i++
		}
		if i < len(args) {
			i++
		}
		return knownAgentFromArgs(args[i:])
	case "stdbuf":
		i := 1
		for i < len(args) && strings.HasPrefix(args[i], "-") {
			i++
		}
		return knownAgentFromArgs(args[i:])
	}

	if !isInvocationWrapper(token) || len(args) < 2 {
		return "", false
	}
	next := normalizeProcessToken(args[1])
	if strings.HasPrefix(next, "-") {
		return "", false
	}
	return knownAgentFromExecutable(next)
}

func knownAgentFromExecutable(token string) (string, bool) {
	token = normalizeProcessToken(token)
	if token == "" {
		return "", false
	}

	base := executableName(token)
	if knownAgents[base] {
		return base, true
	}

	baseNoExt := strings.TrimSuffix(base, filepath.Ext(base))
	if knownAgents[baseNoExt] {
		return baseNoExt, true
	}

	for _, part := range strings.Split(strings.Trim(token, "/"), "/") {
		if normalizeProcessToken(part) == "github-copilot-cli" {
			return "github-copilot-cli", true
		}
	}

	return "", false
}

func normalizeProcessToken(token string) string {
	return strings.Trim(strings.ToLower(token), `"'`)
}

func executableName(token string) string {
	return filepath.Base(normalizeProcessToken(token))
}

func isInvocationWrapper(token string) bool {
	switch executableName(token) {
	case "node", "bun", "deno", "python", "python3", "ruby", "bash", "sh", "zsh", "fish", "dash":
		return true
	default:
		return false
	}
}

func isEnvAssignment(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" || strings.HasPrefix(token, "=") {
		return false
	}
	if strings.HasPrefix(token, "-") {
		return false
	}
	parts := strings.SplitN(token, "=", 2)
	return len(parts) == 2 && parts[0] != ""
}
