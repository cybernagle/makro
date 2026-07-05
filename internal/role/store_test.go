package role

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewStore(t *testing.T) {
	roles := []Role{
		{Name: "makro", Description: "a"},
		{Name: "default", Description: "fallback"},
	}
	s := NewStore(roles)

	assert.Equal(t, 2, s.Len())
	assert.True(t, s.Has("makro"))
	assert.False(t, s.Has("nope"))
}

func TestStoreGet(t *testing.T) {
	s := NewStore([]Role{{Name: "makro", Description: "a"}})
	r, ok := s.Get("makro")
	require.True(t, ok)
	assert.Equal(t, "makro", r.Name)

	_, ok = s.Get("nope")
	assert.False(t, ok)
}

func TestStoreAll(t *testing.T) {
	roles := []Role{
		{Name: "makro", Description: "a"},
		{Name: "juli", Description: "b"},
	}
	s := NewStore(roles)
	all := s.All()
	assert.Len(t, all, 2)
	// All returns a copy; mutating it must not affect the store.
	all[0].Name = "mutated"
	assert.True(t, s.Has("makro"), "store unaffected by mutation of All() result")
}

func TestStoreDefault(t *testing.T) {
	t.Run("named default wins", func(t *testing.T) {
		s := NewStore([]Role{
			{Name: "makro", Description: "a"},
			{Name: "default", Description: "fallback"},
		})
		d, ok := s.Default()
		require.True(t, ok)
		assert.Equal(t, "default", d.Name)
	})

	t.Run("no named default returns first", func(t *testing.T) {
		s := NewStore([]Role{
			{Name: "makro", Description: "a"},
			{Name: "juli", Description: "b"},
		})
		d, ok := s.Default()
		require.True(t, ok)
		assert.Equal(t, "makro", d.Name)
	})

	t.Run("empty store no default", func(t *testing.T) {
		s := NewStore(nil)
		_, ok := s.Default()
		assert.False(t, ok)
	})
}
