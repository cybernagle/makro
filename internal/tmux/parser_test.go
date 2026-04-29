package tmux

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseOutput(t *testing.T) {
	n, err := ParseNotification("%output %0 hello world")
	require.NoError(t, err)
	assert.Equal(t, NotifOutput, n.Type)
	assert.Equal(t, "%0", n.PaneID)
	assert.Equal(t, "hello world", n.Data)
}

func TestParseOutputEscaped(t *testing.T) {
	n, err := ParseNotification("%output %1 \\012\\015\\033")
	require.NoError(t, err)
	assert.Equal(t, NotifOutput, n.Type)
	assert.Equal(t, "%1", n.PaneID)
	assert.Equal(t, "\n\r\033", n.Data)
}

func TestParseOutputMissingPaneID(t *testing.T) {
	_, err := ParseNotification("%output")
	assert.Error(t, err)
}

func TestParseExtendedOutput(t *testing.T) {
	n, err := ParseNotification("%extended-output %0 500 : some output")
	require.NoError(t, err)
	assert.Equal(t, NotifExtendedOutput, n.Type)
	assert.Equal(t, "%0", n.PaneID)
	assert.Equal(t, uint64(500), n.Age)
	assert.Equal(t, "some output", n.Data)
}

func TestParseSessionChanged(t *testing.T) {
	n, err := ParseNotification("%session-changed $1 my-session")
	require.NoError(t, err)
	assert.Equal(t, NotifSessionChanged, n.Type)
	assert.Equal(t, "$1", n.SessionID)
	assert.Equal(t, "my-session", n.SessionName)
}

func TestParseSessionRenamed(t *testing.T) {
	n, err := ParseNotification("%session-renamed $1 new-name")
	require.NoError(t, err)
	assert.Equal(t, NotifSessionRenamed, n.Type)
	assert.Equal(t, "$1", n.SessionID)
	assert.Equal(t, "new-name", n.SessionName)
}

func TestParseSessionsChanged(t *testing.T) {
	n, err := ParseNotification("%sessions-changed")
	require.NoError(t, err)
	assert.Equal(t, NotifSessionsChanged, n.Type)
}

func TestParseSessionWindowChanged(t *testing.T) {
	n, err := ParseNotification("%session-window-changed $1 @3")
	require.NoError(t, err)
	assert.Equal(t, NotifSessionWindowChanged, n.Type)
	assert.Equal(t, "$1", n.SessionID)
	assert.Equal(t, "@3", n.WindowID)
}

func TestParseWindowAdd(t *testing.T) {
	n, err := ParseNotification("%window-add @1")
	require.NoError(t, err)
	assert.Equal(t, NotifWindowAdd, n.Type)
	assert.Equal(t, "@1", n.WindowID)
}

func TestParseWindowClose(t *testing.T) {
	n, err := ParseNotification("%window-close @2")
	require.NoError(t, err)
	assert.Equal(t, NotifWindowClose, n.Type)
	assert.Equal(t, "@2", n.WindowID)
}

func TestParseWindowRenamed(t *testing.T) {
	n, err := ParseNotification("%window-renamed @1 editor")
	require.NoError(t, err)
	assert.Equal(t, NotifWindowRenamed, n.Type)
	assert.Equal(t, "@1", n.WindowID)
	assert.Equal(t, "editor", n.SessionName)
}

func TestParseWindowPaneChanged(t *testing.T) {
	n, err := ParseNotification("%window-pane-changed @1 %0")
	require.NoError(t, err)
	assert.Equal(t, NotifWindowPaneChanged, n.Type)
	assert.Equal(t, "@1", n.WindowID)
	assert.Equal(t, "%0", n.PaneID)
}

func TestParseLayoutChange(t *testing.T) {
	n, err := ParseNotification("%layout-change @1 0000x00,0{0},0{0} 0000x00,0{0},0{0} 0")
	require.NoError(t, err)
	assert.Equal(t, NotifLayoutChange, n.Type)
	assert.Equal(t, "@1", n.WindowID)
	assert.Equal(t, "0000x00,0{0},0{0}", n.Layout)
	assert.Equal(t, "0000x00,0{0},0{0}", n.VisibleLayout)
	assert.Equal(t, "0", n.RawFlags)
}

func TestParseClientSessionChanged(t *testing.T) {
	n, err := ParseNotification("%client-session-changed /dev/ttys000 $1 my-session")
	require.NoError(t, err)
	assert.Equal(t, NotifClientSessionChanged, n.Type)
	assert.Equal(t, "/dev/ttys000", n.ClientName)
	assert.Equal(t, "$1", n.SessionID)
	assert.Equal(t, "my-session", n.SessionName)
}

func TestParseClientDetached(t *testing.T) {
	n, err := ParseNotification("%client-detached /dev/ttys000")
	require.NoError(t, err)
	assert.Equal(t, NotifClientDetached, n.Type)
	assert.Equal(t, "/dev/ttys000", n.ClientName)
}

func TestParsePaneModeChanged(t *testing.T) {
	n, err := ParseNotification("%pane-mode-changed %0")
	require.NoError(t, err)
	assert.Equal(t, NotifPaneModeChanged, n.Type)
	assert.Equal(t, "%0", n.PaneID)
}

func TestParseBegin(t *testing.T) {
	n, err := ParseNotification("%begin 1700000000 42 1")
	require.NoError(t, err)
	assert.Equal(t, NotifBegin, n.Type)
	assert.Equal(t, int64(1700000000), n.Timestamp)
	assert.Equal(t, uint(42), n.Number)
	assert.Equal(t, 1, n.Flags)
}

func TestParseEnd(t *testing.T) {
	n, err := ParseNotification("%end 1700000000 42 0")
	require.NoError(t, err)
	assert.Equal(t, NotifEnd, n.Type)
	assert.Equal(t, int64(1700000000), n.Timestamp)
	assert.Equal(t, uint(42), n.Number)
	assert.Equal(t, 0, n.Flags)
}

func TestParseError(t *testing.T) {
	n, err := ParseNotification("%error 1700000000 42 0")
	require.NoError(t, err)
	assert.Equal(t, NotifError, n.Type)
	assert.Equal(t, int64(1700000000), n.Timestamp)
	assert.Equal(t, uint(42), n.Number)
}

func TestParsePause(t *testing.T) {
	n, err := ParseNotification("%pause %0")
	require.NoError(t, err)
	assert.Equal(t, NotifPause, n.Type)
	assert.Equal(t, "%0", n.PaneID)
}

func TestParseContinue(t *testing.T) {
	n, err := ParseNotification("%continue %0")
	require.NoError(t, err)
	assert.Equal(t, NotifContinue, n.Type)
	assert.Equal(t, "%0", n.PaneID)
}

func TestParseSubscriptionChanged(t *testing.T) {
	n, err := ParseNotification("%subscription-changed some-data-here")
	require.NoError(t, err)
	assert.Equal(t, NotifSubscriptionChanged, n.Type)
	assert.Equal(t, "some-data-here", n.Data)
}

func TestParseEmptyLine(t *testing.T) {
	_, err := ParseNotification("")
	assert.Equal(t, ErrUnknownNotification, err)
}

func TestParseNonNotification(t *testing.T) {
	_, err := ParseNotification("regular output")
	assert.Equal(t, ErrUnknownNotification, err)
}

func TestParseUnknown(t *testing.T) {
	n, err := ParseNotification("%something-new arg1 arg2")
	require.NoError(t, err)
	assert.Equal(t, NotifUnknown, n.Type)
	assert.Equal(t, "%something-new arg1 arg2", n.Data)
}

func TestNotifTypeString(t *testing.T) {
	assert.Equal(t, "output", NotifOutput.String())
	assert.Equal(t, "session-changed", NotifSessionChanged.String())
	assert.Equal(t, "unknown", NotifType(999).String())
}

func TestParseGuardBadTimestamp(t *testing.T) {
	_, err := ParseNotification("%begin bad 42 0")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timestamp")
}

func TestParseGuardBadNumber(t *testing.T) {
	_, err := ParseNotification("%begin 1700000000 bad 0")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "number")
}

func TestParseGuardBadFlags(t *testing.T) {
	_, err := ParseNotification("%begin 1700000000 42 bad")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "flags")
}

func TestParseGuardMissingFields(t *testing.T) {
	_, err := ParseNotification("%begin 1700000000")
	assert.Error(t, err)
}

func TestDecodeEscape(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "hello", "hello"},
		{"newline", "\\012", "\n"},
		{"carriage return", "\\015", "\r"},
		{"escape", "\\033", "\033"},
		{"mixed", "abc\\012def\\033[0m", "abc\ndef\033[0m"},
		{"backslash alone", "a\\b", "a\\b"},
		{"incomplete escape", "\\01", "\\01"},
		{"null byte", "\\000", "\x00"},
		{"tab", "\\011", "\t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DecodeEscape(tt.input))
		})
	}
}

func TestEncodeEscape(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{"plain", []byte("hello"), "hello"},
		{"newline", []byte("\n"), "\\012"},
		{"escape", []byte("\033"), "\\033"},
		{"backslash", []byte("\\"), "\\134"},
		{"mixed", []byte("abc\ndef\033[0m"), "abc\\012def\\033[0m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, EncodeEscape(tt.input))
		})
	}
}

func TestEscapeRoundtrip(t *testing.T) {
	input := []byte("hello\nworld\r\n\t\033[31mred\033[0m")
	encoded := EncodeEscape(input)
	decoded := DecodeEscape(encoded)
	assert.Equal(t, string(input), decoded)
}
