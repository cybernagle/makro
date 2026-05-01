package tmux

// ServerInfo describes a detected tmux server.
type ServerInfo struct {
	SocketPath string
	Sessions   []string
}
