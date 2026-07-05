package role

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/naglezhang/makro/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProvider implements llm.Provider for Router tests. It returns a canned
// CompleteResult for any Complete call.
type fakeProvider struct {
	resp  *llm.CompleteResult
	err   error
	calls int
	last  []llm.Message
}

func (f *fakeProvider) Stream(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (<-chan llm.StreamEvent, error) {
	return nil, nil
}
func (f *fakeProvider) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (*llm.CompleteResult, error) {
	f.calls++
	f.last = m
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}
func (f *fakeProvider) Name() string { return "fake" }

func routerJSON(t *testing.T, role string, conf float64) string {
	t.Helper()
	b, err := json.Marshal(routingResponse{Role: role, Confidence: conf, Reason: "test"})
	require.NoError(t, err)
	return string(b)
}

func TestRouterPicksMatchingRole(t *testing.T) {
	store := NewStore([]Role{
		{Name: "makro", Description: "Makro project development"},
		{Name: "juli", Description: "juli project maintenance"},
	})
	rt := NewRouter(store, &fakeProvider{resp: &llm.CompleteResult{Content: routerJSON(t, "makro", 0.9)}})

	dec, err := rt.Route(context.Background(), "fix the bug in orchestrator")
	require.NoError(t, err)
	assert.Equal(t, "makro", dec.RoleName)
	assert.InDelta(t, 0.9, dec.Confidence, 0.001)
	assert.False(t, dec.Fallback)
}

func TestRouterLowConfidenceFallsBack(t *testing.T) {
	store := NewStore([]Role{
		{Name: "makro", Description: "a"},
		{Name: "default", Description: "fallback"},
	})
	rt := NewRouter(store, &fakeProvider{resp: &llm.CompleteResult{Content: routerJSON(t, "makro", 0.2)}})

	dec, err := rt.Route(context.Background(), "vague task")
	require.NoError(t, err)
	assert.True(t, dec.Fallback, "below threshold → fallback")
	assert.Equal(t, "default", dec.RoleName)
}

func TestRouterLLMReturnsUnknownRoleFallsBack(t *testing.T) {
	store := NewStore([]Role{{Name: "makro", Description: "a"}})
	rt := NewRouter(store, &fakeProvider{resp: &llm.CompleteResult{Content: routerJSON(t, "ghost", 0.99)}})

	dec, err := rt.Route(context.Background(), "task")
	require.NoError(t, err)
	assert.True(t, dec.Fallback)
	assert.Equal(t, "makro", dec.RoleName, "unknown role → store default")
}

func TestRouterMalformedJSONFallsBack(t *testing.T) {
	store := NewStore([]Role{{Name: "makro", Description: "a"}})
	rt := NewRouter(store, &fakeProvider{resp: &llm.CompleteResult{Content: "not json"}})

	dec, err := rt.Route(context.Background(), "task")
	require.NoError(t, err, "malformed JSON is recoverable: fall back, don't error")
	assert.True(t, dec.Fallback)
}

func TestRouterLLMErrorFallsBack(t *testing.T) {
	store := NewStore([]Role{{Name: "makro", Description: "a"}})
	rt := NewRouter(store, &fakeProvider{err: assert.AnError})

	dec, err := rt.Route(context.Background(), "task")
	require.NoError(t, err, "LLM error is recoverable: fall back, don't surface to user")
	assert.True(t, dec.Fallback)
}

func TestRouterEmptyStoreNoFallback(t *testing.T) {
	// No roles at all → no routing possible. The decision is empty; the
	// caller (Dispatcher) treats this as "route to handleLLM" (no-op).
	rt := NewRouter(NewStore(nil), &fakeProvider{})
	dec, err := rt.Route(context.Background(), "task")
	require.NoError(t, err)
	assert.Equal(t, "", dec.RoleName)
	assert.True(t, dec.NoRoles)
	assert.Equal(t, 0, rt.provider.(*fakeProvider).calls, "no LLM call when store is empty")
}

func TestRouterThresholdBoundaryAccepted(t *testing.T) {
	// Confidence exactly at the threshold (0.5) must be ACCEPTED, not fallen
	// back. Pins the < vs <= comparison so a refactor can't silently flip it.
	store := NewStore([]Role{{Name: "makro", Description: "a"}})
	rt := NewRouter(store, &fakeProvider{resp: &llm.CompleteResult{
		Content: routerJSON(t, "makro", 0.5)}})

	dec, err := rt.Route(context.Background(), "task")
	require.NoError(t, err)
	assert.False(t, dec.Fallback, "confidence == threshold (0.5) must accept")
	assert.Equal(t, "makro", dec.RoleName)
}

func TestRouterMultiObjectResponseFallsBack(t *testing.T) {
	// If the LLM emits two JSON objects, stripFences's first-{...}-to-last-}
	// span produces invalid JSON. This must degrade to fallback, not panic.
	store := NewStore([]Role{{Name: "makro", Description: "a"}})
	rt := NewRouter(store, &fakeProvider{resp: &llm.CompleteResult{
		Content: `{"note":"thinking"} {"role":"makro","confidence":0.9,"reason":"x"}`,
	}})

	dec, err := rt.Route(context.Background(), "task")
	require.NoError(t, err, "multi-object response is recoverable: fall back")
	assert.True(t, dec.Fallback)
}
