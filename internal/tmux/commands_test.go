package tmux

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewSessionCmd(t *testing.T) {
	assert.Equal(t, `new-session -d -s test`, NewSessionCmd("test", "", ""))
	assert.Equal(t, `new-session -d -s test -c /tmp/work`, NewSessionCmd("test", "/tmp/work", ""))
	assert.Equal(t, `new-session -d -s test -c /tmp bash`, NewSessionCmd("test", "/tmp", "bash"))
	assert.Equal(t, `new-session -d -s "my session"`, NewSessionCmd("my session", "", ""))
}

func TestKillSessionCmd(t *testing.T) {
	assert.Equal(t, `kill-session -t test`, KillSessionCmd("test"))
	assert.Equal(t, `kill-session -t "my session"`, KillSessionCmd("my session"))
}

func TestSwitchClientCmd(t *testing.T) {
	assert.Equal(t, `switch-client -t auth`, SwitchClientCmd("auth"))
}

func TestSendKeysCmd(t *testing.T) {
	assert.Equal(t, `send-keys -t test "echo hello"`, SendKeysCmd("test", "echo hello"))
	assert.Equal(t, `send-keys -t test C-c`, SendKeysCmd("test", "C-c"))
}

func TestSendKeysLiteralCmd(t *testing.T) {
	assert.Equal(t, `send-keys -t test -l "type this"`, SendKeysLiteralCmd("test", "type this"))
}

func TestSendEnterCmd(t *testing.T) {
	assert.Equal(t, `send-keys -t test Enter`, SendEnterCmd("test"))
}

func TestRenameSessionCmd(t *testing.T) {
	assert.Equal(t, `rename-session -t old new`, RenameSessionCmd("old", "new"))
}

func TestListSessionsCmd(t *testing.T) {
	assert.Equal(t, `list-sessions`, ListSessionsCmd())
}

func TestCapturePaneCmd(t *testing.T) {
	assert.Equal(t, `capture-pane -t %0 -p`, CapturePaneCmd("%0"))
}

func TestResizeWindowCmd(t *testing.T) {
	assert.Equal(t, `resize-window -t test -x 120 -y 40`, ResizeWindowCmd("test", 120, 40))
}

func TestHasSessionCmd(t *testing.T) {
	assert.Equal(t, `has-session -t test`, HasSessionCmd("test"))
}

func TestQuoteArg(t *testing.T) {
	assert.Equal(t, `simple`, quoteArg("simple"))
	assert.Equal(t, `"has space"`, quoteArg("has space"))
	assert.Equal(t, `"has\"quote"`, quoteArg(`has"quote`))
	assert.Equal(t, `"has\\backslash"`, quoteArg(`has\backslash`))
}
