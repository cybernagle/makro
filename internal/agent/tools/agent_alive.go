package tools

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/naglezhang/fingersaver/internal/tmux"
)

// knownAgents are the coding agent binary names we recognize.
var knownAgents = map[string]bool{
	"claude":  true,
	"copilot": true,
	"codex":   true,
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
	cmdBase := filepath.Base(cmd)

	// Direct match: agent is the foreground process.
	if knownAgents[cmdBase] {
		return AgentStatus{Alive: true, Agent: cmdBase, Reason: "agent is running"}
	}

	// If it's a shell, agent might be dead or running a subprocess.
	if knownShells[cmdBase] {
		return checkProcessTree(tc, sessionName)
	}

	// Unknown process — could be agent running a tool (go, node, python, etc.)
	// Check process tree to be sure.
	return checkProcessTree(tc, sessionName)
}

// psCache avoids running ps on every tool call.
var (
	psCache     map[int]string // pid -> command
	psCacheOnce sync.Once
)

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
	pid  string
	ppid string
	cmd  string
}

// buildProcessTree parses ps output into a parent -> children map.
func buildProcessTree() (map[string][]procEntry, error) {
	out, err := exec.Command("ps", "-o", "pid=,ppid=,comm=", "-ax").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}

	children := make(map[string][]procEntry)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, ppid, cmd := fields[0], fields[1], fields[2]
		children[ppid] = append(children[ppid], procEntry{pid: pid, ppid: ppid, cmd: filepath.Base(cmd)})
	}
	return children, nil
}

// findAgentInTree recursively walks the process tree from rootPID
// looking for any process matching a known agent name.
func findAgentInTree(tree map[string][]procEntry, rootPID string) (string, bool) {
	var walk func(pid string) (string, bool)
	walk = func(pid string) (string, bool) {
		for _, child := range tree[pid] {
			if knownAgents[child.cmd] {
				return child.cmd, true
			}
			if agent, found := walk(child.pid); found {
				return agent, found
			}
		}
		return "", false
	}
	return walk(rootPID)
}
