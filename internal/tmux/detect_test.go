//go:build !windows

package tmux

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSocketFromEnv(t *testing.T) {
	orig := os.Getenv("TMUX")
	defer os.Setenv("TMUX", orig)

	os.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	assert.Equal(t, "/tmp/tmux-501/default", socketFromEnv())

	os.Setenv("TMUX", "")
	assert.Equal(t, "", socketFromEnv())

	os.Setenv("TMUX", "nocomma")
	assert.Equal(t, "", socketFromEnv())
}

func TestDefaultSocketPath(t *testing.T) {
	path := defaultSocketPath()
	assert.Contains(t, path, "/tmux-")
	assert.Contains(t, path, "/default")
}

func TestDetectServerNone(t *testing.T) {
	orig := os.Getenv("TMUX")
	defer os.Setenv("TMUX", orig)
	os.Setenv("TMUX", "")

	info := DetectServer()
	if info != nil {
		t.Skip("tmux server is running, skipping nil-detection test")
	}
	assert.Nil(t, info)
}
