package skills

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSkill(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *Skill
		wantErr bool
	}{
		{
			name: "full frontmatter",
			input: `---
name: review
description: Review a PR
allowed-tools:
  - list_sessions
  - read_session_output
arguments:
  - pr_number
---
You are reviewing PR #{{.Arg0}}.
Read the session output and provide feedback.`,
			want: &Skill{
				Name:         "review",
				Description:  "Review a PR",
				Prompt:       "You are reviewing PR #{{.Arg0}}.\nRead the session output and provide feedback.",
				AllowedTools: []string{"list_sessions", "read_session_output"},
				Args:         []string{"pr_number"},
			},
		},
		{
			name: "minimal frontmatter",
			input: `---
name: hello
---
Say hello to the user.`,
			want: &Skill{
				Name:   "hello",
				Prompt: "Say hello to the user.",
			},
		},
		{
			name: "no frontmatter returns error",
			input: `This is just markdown without frontmatter.
---
name: foo`,
			wantErr: true,
		},
		{
			name:    "empty content returns error",
			input:   "",
			wantErr: true,
		},
		{
			name: "description only frontmatter",
			input: `---
name: deploy
description: Deploy to production
---
Deploy the current branch to production.`,
			want: &Skill{
				Name:        "deploy",
				Description: "Deploy to production",
				Prompt:      "Deploy the current branch to production.",
			},
		},
		{
			name: "malformed YAML frontmatter returns error",
			input: `---
name: review
description: [
---
Body here.`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSkill(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExpandPrompt(t *testing.T) {
	skill := &Skill{
		Name:   "review",
		Prompt: "Review PR #{{.Arg0}} with description: {{.Arg1}}. All args: {{.Args}}",
	}

	result := skill.ExpandPrompt([]string{"123", "fix-login"})
	assert.Equal(t, "Review PR #123 with description: fix-login. All args: 123 fix-login", result)
}

func TestExpandPromptNoArgs(t *testing.T) {
	skill := &Skill{
		Name:   "list",
		Prompt: "List all sessions. Args: {{.Args}}",
	}

	result := skill.ExpandPrompt(nil)
	assert.Equal(t, "List all sessions. Args: ", result)
}

func TestExpandPromptFewerArgsThanTemplate(t *testing.T) {
	skill := &Skill{
		Name:   "deploy",
		Prompt: "Deploy {{.Arg0}} to {{.Arg1}} with {{.Arg2}}",
	}

	result := skill.ExpandPrompt([]string{"api"})
	assert.Equal(t, "Deploy api to  with ", result)
}
