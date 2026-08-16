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
//
// Agent identity is MERGE-PRESERVED: if a session's agent has died since the
// last snapshot (pane shows a shell), the last-known ClaudeSession/ClaudeCwd
// are carried forward instead of being overwritten by the bare-shell reading —
// otherwise the 5-minute loop would erase exactly the data recovery needs.
// Sessions that vanished from tmux entirely (crash/reboot) are also kept, so
// post-reboot recovery still knows what ran where.
func TakeSnapshot() (*Snapshot, error) {
	prev, _ := LoadSnapshot()
	prevByName := map[string]SessionSnapshot{}
	for _, ss := range prev.Sessions {
		prevByName[ss.Name] = ss
	}

	out, err := exec.Command(getTmuxBin(), tmuxArgs(
		"list-sessions",
		"-F", "#{session_name}\t#{pane_current_path}\t#{pane_current_command}\t#{pane_pid}\t#{session_attached}",
	)...).CombinedOutput()
	if err != nil {
		s := string(out)
		if strings.Contains(s, "no server running") || strings.Contains(s, "no sessions") ||
			strings.Contains(s, "No such file") || strings.Contains(s, "connect failed") {
			// tmux not running or no sessions — still save the previous
			// sessions (stale) so recovery code has something to compare
			// against, rather than erasing the recovery data.
			stale := &Snapshot{SnapshotAt: time.Now()}
			if prev != nil {
				stale.Sessions = prev.Sessions
			}
			_ = saveSnapshot(stale)
			return stale, nil
		}
		return nil, fmt.Errorf("list-sessions: %s: %w", strings.TrimSpace(s), err)
	}

	snap := &Snapshot{SnapshotAt: time.Now(), Sessions: []SessionSnapshot{}}
	seen := map[string]bool{}
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
		seen[name] = true

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
		} else if p, ok := prevByName[name]; ok && snapshotHadAgent(p) {
			// Agent died since the last snapshot — keep the last-known agent
			// identity so recovery (and the healer) can restore it.
			ss.ClaudeSession = p.ClaudeSession
			ss.ClaudeCwd = p.ClaudeCwd
			ss.Command = p.Command
		}

		snap.Sessions = append(snap.Sessions, ss)
	}

	// Sessions no longer in tmux (server crashed / machine rebooted) stay in
	// the snapshot with their last-known state until tmux comes back.
	for name, p := range prevByName {
		if !seen[name] {
			snap.Sessions = append(snap.Sessions, p)
		}
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
// instance. Matches "claude", "claude.exe" (some installs report the binary
// with an extension), "node /path/to/claude", etc.
func isClaudeCommand(cmd string) bool {
	c := strings.ToLower(cmd)
	if c == "claude" {
		return true
	}
	// Some installs run claude via node — check the binary basename, with any
	// extension stripped so "claude.exe" matches too.
	for _, token := range strings.Fields(c) {
		base := filepath.Base(token)
		base = strings.TrimSuffix(base, filepath.Ext(base))
		if base == "claude" {
			return true
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

// RecoverFromSnapshot is called on server startup. It reconciles live tmux
// sessions against the snapshot: after a macOS reboot tmux is gone entirely,
// and after a Makro restart the viewer's attach-or-create path may already
// have recreated sessions as BARE SHELLS — either way, every snapshot session
// that used to run a coding agent must get its agent back (resume the recorded
// claude session when we captured one, else launch a fresh agent in the
// recorded working dir). Returns the number of sessions that needed repair.
func RecoverFromSnapshot() int {
	snap, err := LoadSnapshot()
	if err != nil || snap == nil || len(snap.Sessions) == 0 {
		return 0
	}
	return reconcileSnapshot(snap)
}

// reconcileSnapshot brings every snapshotted agent session back to health:
// missing session → recreate + launch agent; existing session with a bare
// shell → launch agent into it. Sessions already running an agent are left
// alone. Returns the number of sessions repaired.
func reconcileSnapshot(snap *Snapshot) int {
	live := liveSessionSet()
	recovered := 0
	for _, ss := range snap.Sessions {
		if !snapshotHadAgent(ss) {
			continue // plain-shell sessions recover as plain shells — nothing to do
		}
		if live[ss.Name] && sessionPaneHasAgent(ss.Name) {
			continue // healthy: session exists AND its agent is running
		}
		if !live[ss.Name] {
			if recoverSession(ss) {
				recovered++
			}
			continue
		}
		// Session exists but its agent is gone (bare shell after restart):
		// relaunch the agent inside the existing session.
		if launchAgentInSession(ss) {
			recovered++
		}
	}
	if recovered > 0 {
		log.Printf("[snapshot] recovered agents in %d session(s) from snapshot at %s", recovered, snap.SnapshotAt.Format(time.RFC3339))
	}
	return recovered
}

// StartReconcileLoop keeps snapshotted agent sessions healthy while the server
// runs: an agent that crashes mid-day is relaunched within one interval,
// programmatically — session recovery is the system's job, not something an
// orchestrating LLM (which has no tool to start an agent) or the user should
// be responsible for. onRecovered, when non-nil, is called after any pass that
// repaired something (e.g. to notify the UI).
func StartReconcileLoop(ctx context.Context, interval time.Duration, onRecovered func(n int)) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				snap, err := LoadSnapshot()
				if err != nil || snap == nil {
					continue
				}
				if n := reconcileSnapshot(snap); n > 0 && onRecovered != nil {
					onRecovered(n)
				}
			}
		}
	}()
}

// SnapshotHealer implements tools.SessionHealer: on demand (a gated send just
// found a bare shell), it relaunches the session's agent from the last-known
// snapshot state. Readiness waiting is the caller's job (tools.WaitForAgent).
type SnapshotHealer struct{}

func NewSnapshotHealer() *SnapshotHealer { return &SnapshotHealer{} }

// HealSession relaunches the coding agent recorded for `session`. Errors when
// the session has no recorded agent (nothing to restore) or the launch line
// couldn't be delivered.
func (h *SnapshotHealer) HealSession(session string) error {
	snap, err := LoadSnapshot()
	if err != nil || snap == nil {
		return fmt.Errorf("no session snapshot available for recovery")
	}
	for _, ss := range snap.Sessions {
		if ss.Name != session {
			continue
		}
		if !snapshotHadAgent(ss) {
			return fmt.Errorf("session %q has no recorded agent to restore", session)
		}
		if !launchAgentInSession(ss) {
			return fmt.Errorf("session %q: agent launch failed", session)
		}
		// Brief foreground wait so back-to-back heals can't double-launch:
		// once the agent takes the pane, a concurrent heal sees it healthy.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if sessionPaneHasAgent(session) {
				return nil
			}
			time.Sleep(250 * time.Millisecond)
		}
		return nil
	}
	return fmt.Errorf("session %q not in snapshot", session)
}

// snapshotHadAgent reports whether the snapshotted session was running a
// coding agent (we only know how to restore claude today).
func snapshotHadAgent(ss SessionSnapshot) bool {
	return isClaudeCommand(ss.Command) || ss.ClaudeSession != ""
}

// agentLaunchCommand returns the shell line that (re)starts the agent for a
// snapshotted session, or "" when the snapshot says it ran no agent.
// Preference: resume the exact recorded claude session (same transcript);
// fall back to a fresh agent in the recorded working dir.
func agentLaunchCommand(ss SessionSnapshot) string {
	if !snapshotHadAgent(ss) {
		return ""
	}
	if ss.ClaudeSession != "" && ss.ClaudeCwd != "" {
		return fmt.Sprintf("cd %q && claude --resume %s", ss.ClaudeCwd, ss.ClaudeSession)
	}
	if ss.ClaudeCwd != "" {
		return fmt.Sprintf("cd %q && claude", ss.ClaudeCwd)
	}
	if ss.WorkDir != "" {
		return fmt.Sprintf("cd %q && claude", ss.WorkDir)
	}
	return "claude"
}

// recoverSession recreates a single tmux session from its snapshot, then
// relaunches its agent if it had one. Returns true if the session was created
// (agent-launch failures still count as created — the session shell is up,
// user can start the agent manually).
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
	return launchAgentInSession(ss)
}

// launchAgentInSession types the agent start line into an EXISTING session's
// pane. The freshly spawned shell needs a moment before it reads input, so we
// wait for it to surface as the pane's foreground process first — typing into
// a pane whose zsh is still mid-startup loses or garbles the keystrokes.
func launchAgentInSession(ss SessionSnapshot) bool {
	cmd := agentLaunchCommand(ss)
	if cmd == "" {
		return false
	}
	waitForPaneShell(ss.Name)
	exec.Command(getTmuxBin(), tmuxArgs("send-keys", "-t", ss.Name, "-l", cmd)...).Run()
	exec.Command(getTmuxBin(), tmuxArgs("send-keys", "-t", ss.Name, "Enter")...).Run()
	log.Printf("[snapshot] session %q: launched agent (%s)", ss.Name, agentMode(ss))
	return true
}

// agentMode describes the launch mode for logging.
func agentMode(ss SessionSnapshot) string {
	if ss.ClaudeSession != "" {
		return "claude --resume " + ss.ClaudeSession
	}
	return "fresh claude"
}

// waitForPaneShell polls until the session's pane reports a known shell as its
// foreground process (the shell finished starting and is reading input), up to
// ~5s. Best-effort: on timeout we send anyway — send-keys input is buffered
// by the pty and read once the shell is ready.
func waitForPaneShell(name string) {
	for i := 0; i < 50; i++ {
		out, err := exec.Command(getTmuxBin(), tmuxArgs("display-message", "-p", "-t", name, "#{pane_current_command}")...).Output()
		if err == nil {
			if isShellProcessName(strings.TrimSpace(string(out))) {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// shellProcessNames mirrors the agent tools' shell list: a pane whose
// foreground is one of these holds no coding agent.
var shellProcessNames = map[string]bool{
	"bash": true, "zsh": true, "sh": true, "fish": true, "dash": true,
}

func isShellProcessName(cmd string) bool {
	return shellProcessNames[filepath.Base(strings.TrimSpace(cmd))]
}

// sessionPaneHasAgent reports whether the live session's pane foreground looks
// like a coding agent (anything that isn't a shell — claude, node wrappers,
// copilot, codex). Empty/unknown counts as agent-present to stay
// conservative: we never type into a pane that might be doing something.
func sessionPaneHasAgent(name string) bool {
	out, err := exec.Command(getTmuxBin(), tmuxArgs("display-message", "-p", "-t", name, "#{pane_current_command}")...).Output()
	if err != nil {
		return true // can't inspect — don't touch
	}
	cmd := strings.TrimSpace(string(out))
	if cmd == "" {
		return true // pane not ready — don't touch
	}
	return !isShellProcessName(cmd)
}

// liveSessionSet returns the set of existing tmux session names (empty map if
// no server is running).
func liveSessionSet() map[string]bool {
	out, err := exec.Command(getTmuxBin(), tmuxArgs("list-sessions", "-F", "#{session_name}")...).CombinedOutput()
	live := map[string]bool{}
	if err != nil {
		return live
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			live[line] = true
		}
	}
	return live
}
