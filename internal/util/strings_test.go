package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTruncate(t *testing.T) {
	assert.Equal(t, "short", Truncate("short", 10))
	assert.Equal(t, "0123456789...", Truncate("0123456789012345", 10))
	assert.Equal(t, "你好世...", Truncate("你好世界", 3))
	assert.Equal(t, "abc", Truncate("abc", 5))
}
