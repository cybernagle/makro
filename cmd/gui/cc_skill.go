package main

import (
	"log"
	"os"
	"path/filepath"
)

// makroArtifactsSkill is the Claude Code skill Makro installs into
// ~/.claude/skills/ so that CC — the executor inside each tmux session —
// writes HTML/video deliverables into Makro's central artifact store. CC
// resolves its own tmux session name and writes to
// ~/.makro/artifacts/<session>/; Makro lists that dir for iOS/desktop. This
// keeps Makro out of the scanning/copying business: CC owns the write, Makro
// owns the listing.
const makroArtifactsSkill = `---
name: makro-artifacts
description: When you produce an HTML or video deliverable (report, preview, rendered output, storyboard), write it to Makro's central artifact store so it shows up in the Makro iOS/desktop app.
---

# Makro artifact store

When you produce a deliverable that should be viewable in Makro — an HTML page, a video, or a report you'd otherwise render as HTML — write it under:

    ~/.makro/artifacts/<session>/

where <session> is your current tmux session name. Resolve it with:

    tmux display-message -p '#S'

Create the directory first (mkdir -p). Use a clear filename, e.g. feedback-loop-report.html.

Makro's iOS and desktop apps list this directory per session and render each file in it — anything you write here appears there automatically. Don't bury the shareable copy in a project subfolder; the canonical, viewable copy lives here.
`

// ensureCCSkills installs the makro-artifacts skill into the user's global CC
// skill dir (~/.claude/skills/) if it isn't already present. Idempotent: a
// pre-existing SKILL.md is left untouched so user edits win.
func ensureCCSkills() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".claude", "skills", "makro-artifacts")
	target := filepath.Join(dir, "SKILL.md")
	if _, err := os.Stat(target); err == nil {
		return // already installed — don't clobber user edits
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[cc-skill] mkdir %s: %v", dir, err)
		return
	}
	if err := os.WriteFile(target, []byte(makroArtifactsSkill), 0o644); err != nil {
		log.Printf("[cc-skill] write %s: %v", target, err)
		return
	}
	log.Printf("[cc-skill] installed makro-artifacts skill to %s", target)
}
