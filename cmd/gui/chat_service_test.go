package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/naglezhang/makro/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmitSessionStateBroadcasts verifies the Stage 3 wiring: emitSessionState
// marshals the notifier's per-session state and pushes a session_state event
// onto the chat hub that every connected client reads.
func TestEmitSessionStateBroadcasts(t *testing.T) {
	hub := newChatHub()
	s := &ChatService{hub: hub, notifier: agent.NewAgentNotifier()}
	ch := hub.Subscribe()

	s.emitSessionState("auth")

	evt := <-ch
	assert.Equal(t, "session_state", evt.Type)

	var payload struct {
		Session string `json:"session"`
		Working bool   `json:"working"`
		Unread  int    `json:"unread"`
	}
	require.NoError(t, json.Unmarshal([]byte(evt.Data), &payload))
	assert.Equal(t, "auth", payload.Session)
	assert.False(t, payload.Working, "fresh session → not working")
	assert.Equal(t, 0, payload.Unread, "fresh session → 0 unread")
}

// TestEmitSessionStateNoNotifierMustNotPanic guards the nil-check path
// (chatSvc before init completes).
func TestEmitSessionStateNoNotifierMustNotPanic(t *testing.T) {
	s := &ChatService{hub: newChatHub()}
	assert.NotPanics(t, func() { s.emitSessionState("auth") })
}

// ── Voice-call discuss → propose → dispatch ──

// TestPlanBlockRegexExtractsJSON locks the ```plan parsing contract used by
// maybeStagePlan: the fenced JSON object is captured regardless of surrounding
// conversational prose.
func TestPlanBlockRegexExtractsJSON(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string // expected captured JSON (trimmed)
	}{
		{
			name: "block_with_prose",
			text: "好，我打算给 dev 加重试。\n```plan\n{\"session\":\"dev\",\"summary\":\"s\",\"brief\":\"b\"}\n```\n要我落实吗？",
			want: `{"session":"dev","summary":"s","brief":"b"}`,
		},
		{
			name: "block_only",
			text: "```plan\n" + `{"session":"auth","summary":"s","brief":"b"}` + "\n```",
			want: `{"session":"auth","summary":"s","brief":"b"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := planBlockRe.FindStringSubmatch(tc.text)
			require.NotNil(t, m, "should match a ```plan block")
			assert.Equal(t, tc.want, strings.TrimSpace(m[1]))
		})
	}
}

// TestPlanBlockRegexNoMatch: plain discussion replies with no plan block must
// not match (so they don't get mis-staged).
func TestPlanBlockRegexNoMatch(t *testing.T) {
	for _, text := range []string{
		"我需要再想想这个方案。",
		"```python\nprint(1)\n```",
		"",
	} {
		assert.Nil(t, planBlockRe.FindStringSubmatch(text))
	}
}

// TestMaybeStagePlanStagesAndEmits: a valid plan block stages the plan and emits
// the plan + phase(proposed) events to every client.
func TestMaybeStagePlanStagesAndEmits(t *testing.T) {
	hub := newChatHub()
	ch := hub.Subscribe()
	s := &ChatService{hub: hub}
	reply := "我打算给 dev 加指数退避重试。\n```plan\n{\"session\":\"dev\",\"summary\":\"加指数退避重试\",\"brief\":\"在 login 加重试,最多3次\"}\n```\n要我落实吗？"

	s.maybeStagePlan(reply)

	require.NotNil(t, s.stagedPlan)
	assert.Equal(t, "dev", s.stagedPlan.Session)
	assert.Equal(t, "加指数退避重试", s.stagedPlan.Summary)
	assert.Equal(t, "在 login 加重试,最多3次", s.stagedPlan.Brief)

	e1 := <-ch
	assert.Equal(t, "plan", e1.Type)
	var payload stagedPlan
	require.NoError(t, json.Unmarshal([]byte(e1.Data), &payload))
	assert.Equal(t, "dev", payload.Session)

	e2 := <-ch
	assert.Equal(t, "phase", e2.Type)
	assert.Equal(t, "proposed", e2.Data)
}

// TestMaybeStagePlanRejectsInvalid: a malformed or incomplete plan block must
// not be staged (no session/brief, or bad JSON).
func TestMaybeStagePlanRejectsInvalid(t *testing.T) {
	s := &ChatService{hub: newChatHub()}
	for _, reply := range []string{
		"```plan\nnot json\n```",
		"```plan\n{\"session\":\"\",\"summary\":\"s\",\"brief\":\"b\"}\n```",   // empty session
		"```plan\n{\"session\":\"dev\",\"summary\":\"s\",\"brief\":\"\"}\n```", // empty brief
		"plain reply, no block",
	} {
		s.stagedPlan = nil
		assert.NotPanics(t, func() { s.maybeStagePlan(reply) })
		assert.Nil(t, s.stagedPlan, "invalid plan must not be staged: %q", reply)
	}
}

// TestIsConfirmPhrase classifies confirm / non-confirm utterances. Ambiguous or
// long discussion text must read as "not confirmed" (defaults back to discussion).
func TestIsConfirmPhrase(t *testing.T) {
	yes := []string{"确认", "落实吧", "确认落实", "yes", "OK", "可以", "好的", "对", "行", "没问题", "confirm"}
	for _, s := range yes {
		assert.True(t, isConfirmPhrase(strings.ToLower(s)), "should confirm: %q", s)
	}
	no := []string{"不要", "取消", "算了", "改一下方案", "我觉得这个 brief 不太对，能不能换成先加日志", "no", "wait"}
	for _, s := range no {
		assert.False(t, isConfirmPhrase(strings.ToLower(s)), "should NOT confirm: %q", s)
	}
}

// TestHandleVoiceConfirmIntentNoPlan: with nothing staged, a voice turn is never
// treated as a confirm (falls through to normal routing).
func TestHandleVoiceConfirmIntentNoPlan(t *testing.T) {
	s := &ChatService{hub: newChatHub()}
	assert.False(t, s.handleVoiceConfirmIntent("确认落实"))
}

// ── Voice-call modes (chat / plan / query) ──

// TestParseCallMode: known modes pass through; anything else defaults to plan.
func TestParseCallMode(t *testing.T) {
	assert.Equal(t, ModeChat, parseCallMode("chat"))
	assert.Equal(t, ModePlan, parseCallMode("plan"))
	assert.Equal(t, ModeQuery, parseCallMode("query"))
	assert.Equal(t, ModePlan, parseCallMode(""), "empty → default plan")
	assert.Equal(t, ModePlan, parseCallMode("bogus"), "unknown → default plan")
}

// TestCallModeConfigs locks the per-mode behavior the gate and staging rely on:
// chat = pure LLM (block everything, never stage); plan = stage plans; query =
// read-only tools, no staging. Each must carry a non-empty prefix.
func TestCallModeConfigs(t *testing.T) {
	require.Contains(t, callModeConfigs, ModeChat)
	require.Contains(t, callModeConfigs, ModePlan)
	require.Contains(t, callModeConfigs, ModeQuery)

	chat := callModeConfigs[ModeChat]
	assert.True(t, chat.BlockAllTools, "chat blocks all tools (pure conversation)")
	assert.False(t, chat.StagePlans, "chat never stages plans")
	assert.NotEmpty(t, chat.Prefix)

	plan := callModeConfigs[ModePlan]
	assert.False(t, plan.BlockAllTools, "plan allows read-only tools during discuss")
	assert.True(t, plan.StagePlans, "plan stages proposed plans for confirm/dispatch")
	assert.NotEmpty(t, plan.Prefix)

	query := callModeConfigs[ModeQuery]
	assert.False(t, query.BlockAllTools, "query allows read-only tools")
	assert.False(t, query.StagePlans, "query never stages plans")
	assert.NotEmpty(t, query.Prefix)
}
