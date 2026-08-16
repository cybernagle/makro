package llm

import (
	"strings"
	"testing"
)

func collect(chunks []string) string {
	var f thinkTagFilter
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(f.Push(c))
	}
	out.WriteString(f.Flush())
	return out.String()
}

func TestThinkFilterStrayCloseTag(t *testing.T) {
	// The observed bug: a normal reply with a trailing stray </think>.
	got := collect([]string{"听的啊，你说不用不用，只是测试一下。", "</think>"})
	if got != "听的啊，你说不用不用，只是测试一下。" {
		t.Errorf("stray close: got %q", got)
	}
}

func TestThinkFilterFullInlineBlock(t *testing.T) {
	got := collect([]string{"<think>用户在测试</think>好的，收到。"})
	if got != "好的，收到。" {
		t.Errorf("inline block: got %q", got)
	}
}

func TestThinkFilterBlockAcrossChunks(t *testing.T) {
	got := collect([]string{"前文。<th", "ink>推理部分", "继续推理</thi", "nk>后文。"})
	if got != "前文。后文。" {
		t.Errorf("split tags: got %q", got)
	}
}

func TestThinkFilterPlainAngleBracketText(t *testing.T) {
	// Legit "<" in text must not be swallowed (or only held one push).
	got := collect([]string{"a < b 和 c", " > d"})
	if got != "a < b 和 c > d" {
		t.Errorf("angle text: got %q", got)
	}
}

func TestThinkFilterUnclosedThinkDropsTail(t *testing.T) {
	got := collect([]string{"可见。<think>没写完的推理"})
	if got != "可见。" {
		t.Errorf("unclosed: got %q", got)
	}
}

func TestThinkFilterEmptyDeltas(t *testing.T) {
	got := collect([]string{"", "你好", ""})
	if got != "你好" {
		t.Errorf("empty deltas: got %q", got)
	}
}

func TestStripThinkTagsNonStreaming(t *testing.T) {
	cases := map[string]string{
		"<think>x</think>正文":      "正文",
		"正文</think>":              "正文",
		"a<think>多行\n推理</think>b": "ab",
		"没闭合<think>吞掉":            "没闭合",
		"干净文本":                    "干净文本",
	}
	for in, want := range cases {
		if got := stripThinkTags(in); got != want {
			t.Errorf("stripThinkTags(%q) = %q, want %q", in, got, want)
		}
	}
}
