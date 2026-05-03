package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseSlashCommand(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantCmd  string
		wantArgs []string
		wantOK   bool
	}{
		{"create", "/create test-session", "create", []string{"test-session"}, true},
		{"create with dir", "/create my-sess /tmp/work", "create", []string{"my-sess", "/tmp/work"}, true},
		{"switch", "/switch auth", "switch", []string{"auth"}, true},
		{"kill", "/kill old-session", "kill", []string{"old-session"}, true},
		{"list", "/list", "list", []string{}, true},
		{"help", "/help", "help", []string{}, true},
		{"no slash", "hello world", "", nil, false},
		{"just slash", "/", "", nil, false},
		{"spaces", "  /switch   auth  ", "switch", []string{"auth"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, args, ok := ParseSlashCommand(tt.input)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantCmd, cmd)
				assert.Equal(t, tt.wantArgs, args)
			}
		})
	}
}

func TestExtractMention(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantSession string
		wantText    string
	}{
		{"simple", "@auth run tests", "auth", "run tests"},
		{"no mention", "just a message", "", "just a message"},
		{"mention only", "@auth", "auth", ""},
		{"mention with spaces", "  @frontend  build  ", "frontend", "build"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session, text := ExtractMention(tt.input)
			assert.Equal(t, tt.wantSession, session)
			assert.Equal(t, tt.wantText, text)
		})
	}
}

func TestCommandRegistryExecute(t *testing.T) {
	mc := newMockTmuxClient()
	cr := NewCommandRegistry(mc, nil)

	result, err := cr.Execute(nil, "/help")
	assert.NoError(t, err)
	assert.Contains(t, result, "create")
	assert.Contains(t, result, "switch")
}

func TestCommandRegistryUnknown(t *testing.T) {
	mc := newMockTmuxClient()
	cr := NewCommandRegistry(mc, nil)

	_, err := cr.Execute(nil, "/unknown")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown command")
}

func TestCommandRegistryNotSlashCommand(t *testing.T) {
	mc := newMockTmuxClient()
	cr := NewCommandRegistry(mc, nil)

	_, err := cr.Execute(nil, "hello")
	assert.Error(t, err)
}
