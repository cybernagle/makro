//go:build windows

package tmux

// DetectServer returns nil on Windows (tmux not available).
func DetectServer() *ServerInfo { return nil }
