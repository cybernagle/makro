package role

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRenderRolesPromptEmpty(t *testing.T) {
	got := RenderRolesPrompt(NewStore(nil))
	assert.Equal(t, "", got, "empty store renders empty string")
}

func TestRenderRolesPromptContainsAllRoles(t *testing.T) {
	store := NewStore([]Role{
		{Name: "review", Description: "代码 review、PR review", Session: "review"},
		{Name: "dev", Description: "Makro 项目的开发", Session: "dev"},
		{Name: "research", Description: "通用调研", Session: ""}, // empty session
	})
	got := RenderRolesPrompt(store)

	// Every role's name, session (or name when empty), and description appears.
	assert.Contains(t, got, "review")
	assert.Contains(t, got, "代码 review、PR review")
	assert.Contains(t, got, "dev")
	assert.Contains(t, got, "Makro 项目的开发")
	// Empty session renders as the role name.
	assert.Contains(t, got, "research")

	// The sentinel marker is present (SetRoles uses it for idempotency).
	assert.True(t, strings.Contains(got, rolesPromptMarker), "rendered prompt must contain the sentinel marker %q", rolesPromptMarker)
}

func TestRenderRolesPromptFormat(t *testing.T) {
	store := NewStore([]Role{
		{Name: "review", Description: "review tasks", Session: "review"},
	})
	got := RenderRolesPrompt(store)

	// Spot-check the structural elements: a header line and a bullet per role.
	assert.Contains(t, got, "session:")
	assert.Contains(t, got, "- review")
}

func TestRolesPromptMarkerAccessor(t *testing.T) {
	m := RolesPromptMarker()
	assert.NotEmpty(t, m, "marker must not be empty")
	assert.Equal(t, rolesPromptMarker, m, "accessor returns the package const")
}
