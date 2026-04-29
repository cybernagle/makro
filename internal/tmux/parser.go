package tmux

import (
	"fmt"
	"strconv"
	"strings"
)

type NotifType int

const (
	NotifOutput NotifType = iota
	NotifExtendedOutput
	NotifSessionChanged
	NotifSessionRenamed
	NotifSessionsChanged
	NotifSessionWindowChanged
	NotifWindowAdd
	NotifWindowClose
	NotifWindowRenamed
	NotifWindowPaneChanged
	NotifLayoutChange
	NotifClientSessionChanged
	NotifClientDetached
	NotifPaneModeChanged
	NotifBegin
	NotifEnd
	NotifError
	NotifPause
	NotifContinue
	NotifSubscriptionChanged
	NotifUnknown
)

func (t NotifType) String() string {
	names := map[NotifType]string{
		NotifOutput:               "output",
		NotifExtendedOutput:       "extended-output",
		NotifSessionChanged:       "session-changed",
		NotifSessionRenamed:       "session-renamed",
		NotifSessionsChanged:      "sessions-changed",
		NotifSessionWindowChanged: "session-window-changed",
		NotifWindowAdd:            "window-add",
		NotifWindowClose:          "window-close",
		NotifWindowRenamed:        "window-renamed",
		NotifWindowPaneChanged:    "window-pane-changed",
		NotifLayoutChange:         "layout-change",
		NotifClientSessionChanged: "client-session-changed",
		NotifClientDetached:       "client-detached",
		NotifPaneModeChanged:      "pane-mode-changed",
		NotifBegin:                "begin",
		NotifEnd:                  "end",
		NotifError:                "error",
		NotifPause:                "pause",
		NotifContinue:             "continue",
		NotifSubscriptionChanged:  "subscription-changed",
		NotifUnknown:              "unknown",
	}
	if s, ok := names[t]; ok {
		return s
	}
	return "unknown"
}

type Notification struct {
	Type          NotifType
	PaneID        string // %0 format
	SessionID     string // $0 format
	WindowID      string // @0 format
	SessionName   string
	ClientName    string
	Data          string
	Timestamp     int64
	Number        uint
	Flags         int
	Age           uint64
	Layout        string
	VisibleLayout string
	RawFlags      string
}

var ErrUnknownNotification = fmt.Errorf("unknown notification format")

// ParseNotification parses a single line from tmux -CC stdout.
func ParseNotification(line string) (Notification, error) {
	if len(line) == 0 || line[0] != '%' {
		return Notification{}, ErrUnknownNotification
	}

	// Remove leading % and split into parts.
	content := line[1:]

	// Commands wrapped in %begin/%end/%error guards.
	if strings.HasPrefix(content, "begin ") {
		return parseGuard(NotifBegin, content[6:])
	}
	if strings.HasPrefix(content, "end ") {
		return parseGuard(NotifEnd, content[4:])
	}
	if strings.HasPrefix(content, "error ") {
		return parseGuard(NotifError, content[6:])
	}

	// Simple keyword-only notifications.
	if content == "sessions-changed" {
		return Notification{Type: NotifSessionsChanged}, nil
	}

	// Keyword + argument notifications.
	keyword, rest, hasSpace := strings.Cut(content, " ")

	switch keyword {
	case "output":
		if !hasSpace {
			return Notification{}, fmt.Errorf("%%output: missing pane id")
		}
		paneID, data, _ := strings.Cut(rest, " ")
		return Notification{Type: NotifOutput, PaneID: paneID, Data: DecodeEscape(data)}, nil

	case "extended-output":
		if !hasSpace {
			return Notification{}, fmt.Errorf("%%extended-output: missing arguments")
		}
		paneID, rest2, _ := strings.Cut(rest, " ")
		ageStr, data, _ := strings.Cut(rest2, " : ")
		age, _ := strconv.ParseUint(ageStr, 10, 64)
		return Notification{Type: NotifExtendedOutput, PaneID: paneID, Age: age, Data: DecodeEscape(data)}, nil

	case "session-changed":
		if !hasSpace {
			return Notification{}, fmt.Errorf("%%session-changed: missing arguments")
		}
		sid, name, _ := strings.Cut(rest, " ")
		return Notification{Type: NotifSessionChanged, SessionID: sid, SessionName: name}, nil

	case "session-renamed":
		if !hasSpace {
			return Notification{}, fmt.Errorf("%%session-renamed: missing arguments")
		}
		sid, name, _ := strings.Cut(rest, " ")
		return Notification{Type: NotifSessionRenamed, SessionID: sid, SessionName: name}, nil

	case "session-window-changed":
		if !hasSpace {
			return Notification{}, fmt.Errorf("%%session-window-changed: missing arguments")
		}
		sid, wid, _ := strings.Cut(rest, " ")
		return Notification{Type: NotifSessionWindowChanged, SessionID: sid, WindowID: wid}, nil

	case "window-add":
		return Notification{Type: NotifWindowAdd, WindowID: rest}, nil

	case "window-close":
		return Notification{Type: NotifWindowClose, WindowID: rest}, nil

	case "window-renamed":
		if !hasSpace {
			return Notification{}, fmt.Errorf("%%window-renamed: missing arguments")
		}
		wid, name, _ := strings.Cut(rest, " ")
		return Notification{Type: NotifWindowRenamed, WindowID: wid, SessionName: name}, nil

	case "window-pane-changed":
		if !hasSpace {
			return Notification{}, fmt.Errorf("%%window-pane-changed: missing arguments")
		}
		wid, pid, _ := strings.Cut(rest, " ")
		return Notification{Type: NotifWindowPaneChanged, WindowID: wid, PaneID: pid}, nil

	case "layout-change":
		if !hasSpace {
			return Notification{}, fmt.Errorf("%%layout-change: missing arguments")
		}
		return parseLayoutChange(rest)

	case "client-session-changed":
		if !hasSpace {
			return Notification{}, fmt.Errorf("%%client-session-changed: missing arguments")
		}
		client, rest2, _ := strings.Cut(rest, " ")
		sid, name, _ := strings.Cut(rest2, " ")
		return Notification{Type: NotifClientSessionChanged, ClientName: client, SessionID: sid, SessionName: name}, nil

	case "client-detached":
		return Notification{Type: NotifClientDetached, ClientName: rest}, nil

	case "pane-mode-changed":
		return Notification{Type: NotifPaneModeChanged, PaneID: rest}, nil

	case "pause":
		return Notification{Type: NotifPause, PaneID: rest}, nil

	case "continue":
		return Notification{Type: NotifContinue, PaneID: rest}, nil

	case "subscription-changed":
		return Notification{Type: NotifSubscriptionChanged, Data: rest}, nil
	}

	return Notification{Type: NotifUnknown, Data: line}, nil
}

// Format: %begin <timestamp> <number> <flags>
func parseGuard(t NotifType, rest string) (Notification, error) {
	parts := strings.SplitN(rest, " ", 3)
	if len(parts) < 3 {
		return Notification{}, fmt.Errorf("%%%s: expected 3 fields, got %d", t, len(parts))
	}
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return Notification{}, fmt.Errorf("%%%s: invalid timestamp: %w", t, err)
	}
	num, err := strconv.ParseUint(parts[1], 10, 0)
	if err != nil {
		return Notification{}, fmt.Errorf("%%%s: invalid number: %w", t, err)
	}
	flags, err := strconv.Atoi(parts[2])
	if err != nil {
		return Notification{}, fmt.Errorf("%%%s: invalid flags: %w", t, err)
	}
	return Notification{Type: t, Timestamp: ts, Number: uint(num), Flags: flags}, nil
}

// Format: @<id> <layout> <visible_layout> <raw_flags>
func parseLayoutChange(rest string) (Notification, error) {
	parts := strings.SplitN(rest, " ", 4)
	if len(parts) < 4 {
		return Notification{}, fmt.Errorf("%%layout-change: expected 4 fields, got %d", len(parts))
	}
	return Notification{
		Type:          NotifLayoutChange,
		WindowID:      parts[0],
		Layout:        parts[1],
		VisibleLayout: parts[2],
		RawFlags:      parts[3],
	}, nil
}

// DecodeEscape decodes tmux \NNN octal escape sequences back to raw bytes.
func DecodeEscape(data string) string {
	var sb strings.Builder
	sb.Grow(len(data))
	i := 0
	for i < len(data) {
		if data[i] == '\\' && i+3 < len(data) {
			if b, err := strconv.ParseUint(data[i+1:i+4], 8, 8); err == nil {
				sb.WriteByte(byte(b))
				i += 4
				continue
			}
		}
		sb.WriteByte(data[i])
		i++
	}
	return sb.String()
}

// EncodeEscape encodes raw bytes into tmux \NNN octal escape format.
func EncodeEscape(data []byte) string {
	var sb strings.Builder
	sb.Grow(len(data) * 2)
	for _, b := range data {
		if b < ' ' || b == '\\' {
			fmt.Fprintf(&sb, "\\%03o", b)
		} else {
			sb.WriteByte(b)
		}
	}
	return sb.String()
}
