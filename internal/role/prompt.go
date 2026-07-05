package role

import (
	"fmt"
	"strings"
)

// rolesPromptMarker wraps the roles section so SetRoles can detect whether it
// has already been appended to the orchestrator's system prompt (idempotency).
// It must appear exactly once in any prompt that includes roles.
const rolesPromptMarker = "<!-- makro:roles -->"

// RenderRolesPrompt renders the store's roles into a "Managed sessions" section
// suitable for appending to the orchestrator's system prompt. Returns "" for
// an empty store (the caller treats empty as "append nothing").
//
// The section is wrapped in rolesPromptMarker so the orchestrator can detect a
// double-append. Format:
//
//	<!-- makro:roles -->
//	You manage the following tmux sessions, each with a role describing what
//	it handles. When the user's task matches a role, send it to that session
//	with send_to_session. If no role clearly fits, handle the task yourself.
//
//	- review (session: review): 代码 review、PR review
//	- dev (session: dev): Makro 项目的开发
//	- research (session: research): 通用调研
//	<!-- makro:roles -->
func RenderRolesPrompt(store *Store) string {
	if store == nil || store.Len() == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(rolesPromptMarker + "\n")
	sb.WriteString("You manage the following tmux sessions, each with a role describing what it handles. ")
	sb.WriteString("When the user's task matches a role, send it to that session with the send_to_session tool. ")
	sb.WriteString("If no role clearly fits, handle the task yourself in this conversation.\n\n")
	for _, r := range store.All() {
		session := r.Session
		if session == "" {
			session = r.Name
		}
		fmt.Fprintf(&sb, "- %s (session: %s): %s\n", r.Name, session, r.Description)
	}
	sb.WriteString(rolesPromptMarker)
	return sb.String()
}

// RolesPromptMarker returns the sentinel string that wraps a rendered roles
// section. The orchestrator's SetRoles uses it to detect whether the roles
// section has already been appended to a system prompt (idempotency).
func RolesPromptMarker() string {
	return rolesPromptMarker
}
