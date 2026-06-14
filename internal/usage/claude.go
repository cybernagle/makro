package usage

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RecordClaudeSession upserts a Claude Code session ↔ tmux-session mapping so
// the ingester knows which transcripts to read and how to attribute them.
func (s *Store) RecordClaudeSession(claudeSessionID, tmuxSession, transcriptPath, cwd string) {
	if s == nil || s.db == nil || claudeSessionID == "" || transcriptPath == "" {
		return
	}
	_, err := s.db.ExecContext(context.Background(),
		`INSERT INTO claude_sessions (claude_session_id, tmux_session, transcript_path, cwd)
		 VALUES (?,?,?,?)
		 ON CONFLICT(claude_session_id) DO UPDATE SET
		   tmux_session=excluded.tmux_session,
		   transcript_path=excluded.transcript_path,
		   cwd=excluded.cwd`,
		claudeSessionID, tmuxSession, transcriptPath, cwd)
	if err != nil {
		log.Printf("[usage] claude session upsert: %v", err)
	}
}

// IngestTranscripts scans every mapped Claude Code transcript (those registered
// via the SessionStart hook) for new assistant turns. Intended for a periodic
// ticker. New sessions only — for already-running sessions see
// IngestProjectTranscripts.
func (s *Store) IngestTranscripts() {
	if s == nil || s.db == nil {
		return
	}
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx,
		`SELECT claude_session_id, tmux_session, transcript_path FROM claude_sessions`)
	if err != nil {
		log.Printf("[usage] ingest query: %v", err)
		return
	}
	type sess struct{ id, tmux, path string }
	var sessions []sess
	for rows.Next() {
		var ss sess
		if rows.Scan(&ss.id, &ss.tmux, &ss.path) == nil {
			sessions = append(sessions, ss)
		}
	}
	rows.Close()
	for _, ss := range sessions {
		name := func(string) string { return ss.tmux }
		s.ingestFile(ctx, ss.id, ss.path, name)
	}
}

// IngestProjectTranscripts scans <claudeProjectsDir>/*/*.jsonl for transcripts
// NOT already mapped to a tmux session, and ingests them attributed by their
// working-directory basename. This is the fallback so already-running Claude
// Code sessions appear in the dashboard without a restart.
func (s *Store) IngestProjectTranscripts(claudeProjectsDir string) {
	if s == nil || s.db == nil || claudeProjectsDir == "" {
		return
	}
	ctx := context.Background()
	matches, err := filepath.Glob(filepath.Join(claudeProjectsDir, "*", "*.jsonl"))
	if err != nil || len(matches) == 0 {
		return
	}
	// Skip transcripts already mapped (handled by IngestTranscripts with a tmux
	// name); they share the offset row keyed by the same session id.
	mapped := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT claude_session_id FROM claude_sessions`)
	if err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				mapped[id] = true
			}
		}
		rows.Close()
	}
	for _, path := range matches {
		stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		if mapped[stem] {
			continue
		}
		s.ingestFile(ctx, stem, path, sessionLabel)
	}
}

// ingestFile reads transcriptPath from its last offset, parses complete lines
// only (a partial trailing line is left for the next poll), logs each assistant
// turn as a prompt_usage record with call_function="claude_code", and advances
// the offset. nameFor resolves each turn's session_name (tmux name for mapped,
// cwd basename for the fallback scan).
func (s *Store) ingestFile(ctx context.Context, claudeID, transcriptPath string, nameFor func(cwd string) string) {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return // not created yet, or rotated away
	}
	defer f.Close()

	var offset int64
	_ = s.db.QueryRowContext(ctx,
		`SELECT byte_offset FROM claude_ingest_offset WHERE claude_session_id=?`, claudeID).Scan(&offset)
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return
		}
	}
	chunk, err := io.ReadAll(f)
	if err != nil || len(chunk) == 0 {
		return
	}

	// Only process up to the last newline — anything after is an in-progress line.
	lastNL := strings.LastIndexByte(string(chunk), '\n')
	if lastNL < 0 {
		return
	}
	lines := strings.Split(string(chunk[:lastNL+1]), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}

	for _, line := range lines {
		rec, cwd, ok := parseAssistantLine([]byte(line))
		if !ok {
			continue
		}
		rec.SessionName = nameFor(cwd)
		rec.CallFunction = "claude_code"
		s.Record(rec)
	}

	newOffset := offset + int64(lastNL+1)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO claude_ingest_offset (claude_session_id, byte_offset, last_ingested_at)
		 VALUES (?,?,?)
		 ON CONFLICT(claude_session_id) DO UPDATE SET byte_offset=excluded.byte_offset, last_ingested_at=excluded.last_ingested_at`,
		claudeID, newOffset, time.Now().Local().Format("2006-01-02 15:04:05"))
	if err != nil {
		log.Printf("[usage] offset update %s: %v", claudeID, err)
	}
}

// parseAssistantLine extracts a usage Record + cwd from one transcript JSONL
// line. Returns ok=false for non-assistant lines or lines without usage.
func parseAssistantLine(line []byte) (rec Record, cwd string, ok bool) {
	var entry struct {
		Type      string `json:"type"`
		UUID      string `json:"uuid"`
		Cwd       string `json:"cwd"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			Model string `json:"model"`
			Usage struct {
				InputTokens              int64 `json:"input_tokens"`
				OutputTokens             int64 `json:"output_tokens"`
				CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &entry) != nil {
		return Record{}, "", false
	}
	if entry.Type != "assistant" {
		return Record{}, "", false
	}
	u := entry.Message.Usage
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return Record{}, "", false
	}
	ts, _ := time.Parse(time.RFC3339, entry.Timestamp)
	// Cache tokens (cache_read/creation) aren't stored — the prompt_usage schema
	// has no columns for them. Only input/output are counted, consistent with the
	// orchestrator records. (Cache tracking is a follow-up if needed.)
	return Record{
		Timestamp:        ts,
		ModelType:        entry.Message.Model,
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.InputTokens + u.OutputTokens,
		CallContext:      entry.UUID, // unique per turn → avoids false duplicate flags
	}, entry.Cwd, true
}

// sessionLabel derives a dashboard session name from a transcript's cwd: the
// working-directory basename (e.g. ".../makro" → "makro"), or "claude" if empty.
func sessionLabel(cwd string) string {
	if cwd == "" {
		return "claude"
	}
	return filepath.Base(cwd)
}
