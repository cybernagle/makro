package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SessionSnapshot captures the recoverable state of a tmux session.
type SessionSnapshot struct {
	Name          string    `json:"name"`
	WorkDir       string    `json:"work_dir"`
	Command       string    `json:"command"`
	PanePID       int       `json:"pane_pid"`
	ClaudeSession string    `json:"claude_session,omitempty"`
	ClaudeCwd     string    `json:"claude_cwd,omitempty"`
	ActiveAt      time.Time `json:"active_at"`
}

// Snapshot is the on-disk format written to ~/.makro/sessions_snapshot.json.
type Snapshot struct {
	SnapshotAt time.Time         `json:"snapshot_at"`
	Sessions   []SessionSnapshot `json:"sessions"`
}

func snapshotPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".makro", "sessions_snapshot.json")
}

// TakeSnapshot inspects all tmux sessions and writes a Snapshot to disk.
// Returns the snapshot and nil on success. Errors are logged but non-fatal —
// snapshot failures should not crash the server.
func TakeSnapshot() (*Snapshot, error) {
	out, err := exec.Command(getTmuxBin(), tmuxArgs(
		"list-sessions",
		"-F", "#{session_name}\t#{pane_current_path}\t#{pane_current_command}\t#{pane_pid}\t#{session_attached}",
	)...).CombinedOutput()
	if err != nil {
		s := string(out)
		if strings.Contains(s, "no server running") || strings.Contains(s, "no sessions") ||
			strings.Contains(s, "No such file") || strings.Contains(s, "connect failed") {
			// tmux not running or no sessions — still save an empty snapshot so
			// recovery code has something to compare against.
			snap := &Snapshot{SnapshotAt: time.Now(), Sessions: []SessionSnapshot{}}
			_ = saveSnapshot(snap)
			return snap, nil
		}
		return nil, fmt.Errorf("list-sessions: %s: %w", strings.TrimSpace(s), err)
	}

	snap := &Snapshot{SnapshotAt: time.Now(), Sessions: []SessionSnapshot{}}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 4 {
			continue
		}
		name := parts[0]
		workDir := parts[1]
		cmd := parts[2]
		var panePID int
		fmt.Sscanf(parts[3], "%d", &panePID)

		ss := SessionSnapshot{
			Name:     name,
			WorkDir:  workDir,
			Command:  cmd,
			PanePID:  panePID,
			ActiveAt: snap.SnapshotAt,
		}

		if isClaudeCommand(cmd) {
			sessionID, claudeCwd := findClaudeSession(panePID)
			ss.ClaudeSession = sessionID
			ss.ClaudeCwd = claudeCwd
		}

		snap.Sessions = append(snap.Sessions, ss)
	}

	if err := saveSnapshot(snap); err != nil {
		return snap, err
	}
	return snap, nil
}

func saveSnapshot(snap *Snapshot) error {
	path := snapshotPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadSnapshot reads the latest snapshot from disk. Returns nil if no snapshot
// exists (not an error).
func LoadSnapshot() (*Snapshot, error) {
	data, err := os.ReadFile(snapshotPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// StartSnapshotLoop runs TakeSnapshot on a fixed interval until ctx is cancelled.
func StartSnapshotLoop(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		// Take an immediate snapshot on startup so we have a baseline.
		if _, err := TakeSnapshot(); err != nil {
			log.Printf("[snapshot] initial failed: %v", err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := TakeSnapshot(); err != nil {
					log.Printf("[snapshot] failed: %v", err)
				}
			}
		}
	}()
}

// isClaudeCommand returns true if the pane command looks like a running claude
// instance. Matches "claude", "node /path/to/claude", etc.
func isClaudeCommand(cmd string) bool {
	c := strings.ToLower(cmd)
	if c == "claude" {
		return true
	}
	// Some installs run claude via node — check the binary basename.
	if strings.HasSuffix(c, "/claude") || strings.Contains(c, "claude") {
		// Avoid matching "claude-code" wrappers falsely; require the token.
		for _, token := range strings.Fields(c) {
			if strings.HasSuffix(filepath.Base(token), "claude") {
				return true
			}
		}
	}
	return false
}

// findClaudeSession walks the process tree under panePID to find a running
// `claude` process. If found, returns (sessionID, claudeCwd) where sessionID
// is the newest .jsonl filename in ~/.claude/projects/<encoded-cwd>/.
//
// Returns ("", "") if claude isn't found or no session can be identified.
func findClaudeSession(panePID int) (string, string) {
	if panePID <= 0 {
		return "", ""
	}
	claudePID := findChildClaude(panePID)
	if claudePID <= 0 {
		return "", ""
	}
	cwd := processCwd(claudePID)
	if cwd == "" {
		return "", ""
	}
	sessionID := newestClaudeSession(cwd)
	return sessionID, cwd
}

// findChildClaude does a BFS through the process tree under root, returning
// the PID of the first process whose name suggests claude.
func findChildClaude(root int) int {
	queue := []int{root}
	visited := map[int]bool{root: true}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		name := processName(pid)
		if pid != root && isClaudeCommand(name) {
			return pid
		}
		for _, child := range childProcesses(pid) {
			if !visited[child] {
				visited[child] = true
				queue = append(queue, child)
			}
		}
	}
	return 0
}

// processName returns the command name for pid, or "" on error.
func processName(pid int) string {
	out, err := exec.Command("ps", "-p", fmt.Sprintf("%d", pid), "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// processCwd returns the current working directory for pid via lsof on macOS.
func processCwd(pid int) string {
	out, err := exec.Command("lsof", "-a", "-p", fmt.Sprintf("%d", pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return strings.TrimPrefix(line, "n")
		}
	}
	return ""
}

// childProcesses returns immediate child PIDs of parent via pgrep -P.
func childProcesses(parent int) []int {
	out, err := exec.Command("pgrep", "-P", fmt.Sprintf("%d", parent)).Output()
	if err != nil {
		return nil
	}
	var children []int
	for _, line := range strings.Fields(string(out)) {
		var pid int
		if _, err := fmt.Sscanf(line, "%d", &pid); err == nil {
			children = append(children, pid)
		}
	}
	return children
}

// newestClaudeSession finds the newest .jsonl under
// ~/.claude/projects/<encoded-cwd>/ and returns its filename without extension.
//
// Claude Code encodes cwd by replacing "/" and "." with "-" and trimming the
// leading dash. e.g. /Users/foo/bar -> -Users-foo-bar
func newestClaudeSession(cwd string) string {
	home, _ := os.UserHomeDir()
	encoded := encodeCwdForClaude(cwd)
	projectDir := filepath.Join(home, ".claude", "projects", encoded)
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return ""
	}
	type info struct {
		name  string
		mtime time.Time
	}
	var files []info
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, info{name: n, mtime: fi.ModTime()})
	}
	if len(files) == 0 {
		return ""
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.After(files[j].mtime) })
	return strings.TrimSuffix(files[0].name, ".jsonl")
}

// encodeCwdForClaude mirrors Claude Code's project-dir encoding:
// replace "/" and "." with "-", leaving a leading "-" for absolute paths.
func encodeCwdForClaude(cwd string) string {
	r := strings.NewReplacer("/", "-", ".", "-")
	return r.Replace(cwd)
}

// RecoverFromSnapshot is called on server startup. If tmux has no socket
// (crashed) and a snapshot exists, recreates each session and resumes claude
// when possible. Returns the number of sessions recovered.
func RecoverFromSnapshot() int {
	sock := tmuxSocketPath()
	if _, err := os.Stat(sock); err == nil {
		// Socket exists — tmux is running. Check if sessions are alive.
		out, _ := exec.Command(getTmuxBin(), tmuxArgs("list-sessions", "-F", "#{session_name}")...).CombinedOutput()
		if !strings.Contains(string(out), "no server running") && !strings.Contains(string(out), "No such file") && strings.TrimSpace(string(out)) != "" {
			return 0
		}
	}

	snap, err := LoadSnapshot()
	if err != nil || snap == nil || len(snap.Sessions) == 0 {
		return 0
	}

	recovered := 0
	for _, ss := range snap.Sessions {
		if recoverSession(ss) {
			recovered++
		}
	}
	log.Printf("[snapshot] recovered %d/%d sessions from snapshot at %s", recovered, len(snap.Sessions), snap.SnapshotAt.Format(time.RFC3339))
	return recovered
}

// recoverSession recreates a single tmux session from its snapshot.
// Returns true if the session was created (claude resume failures still count
// as created — the session shell is up, user can resume manually).
func recoverSession(ss SessionSnapshot) bool {
	args := tmuxArgs("new-session", "-d", "-s", ss.Name)
	if ss.WorkDir != "" {
		if _, err := os.Stat(ss.WorkDir); err == nil {
			args = append(args, "-c", ss.WorkDir)
		}
	}
	if _, err := exec.Command(getTmuxBin(), args...).CombinedOutput(); err != nil {
		log.Printf("[snapshot] recreate session %q failed: %v", ss.Name, err)
		return false
	}

	if ss.ClaudeSession != "" && ss.ClaudeCwd != "" {
		resumeCmd := fmt.Sprintf("cd %q && claude --resume %s", ss.ClaudeCwd, ss.ClaudeSession)
		exec.Command(getTmuxBin(), tmuxArgs("send-keys", "-t", ss.Name, resumeCmd)...).Run()
		exec.Command(getTmuxBin(), tmuxArgs("send-keys", "-t", ss.Name, "Enter")...).Run()
		log.Printf("[snapshot] session %q: resumed claude %s", ss.Name, ss.ClaudeSession)
	}
	return true
}
