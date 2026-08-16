package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnrichHTMLInjectsBeforeHeadClose(t *testing.T) {
	in := []byte("<!DOCTYPE html><html><head><title>Report</title></head><body><p>hi</p></body></html>")
	meta := map[string]any{"title": "周报", "tldr": "本周进展", "created": "2026-08-16", "tags": []any{"review", "share"}}
	out := string(enrichHTMLForSharing(in, meta, "https://example.com/og.png"))

	for _, want := range []string{
		"<!-- makro-meta:start -->",
		`<meta property="og:title" content="周报">`,
		`<meta name="description" content="本周进展">`,
		`<meta property="og:image" content="https://example.com/og.png">`,
		`"headline":"周报"`,
		"<!-- makro-meta:end -->",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("enriched output missing %q\n got: %s", want, out)
		}
	}
	// The block must land in <head>, before the body.
	if i, j := strings.Index(out, "makro-meta:start"), strings.Index(out, "<body"); i > j || j == -1 || i == -1 {
		t.Errorf("meta block not injected before <body> (block@%d, body@%d)", i, j)
	}
}

func TestEnrichHTMLEscapesMetaValues(t *testing.T) {
	in := []byte("<html><head></head><body></body></html>")
	meta := map[string]any{"title": `A "quoted" <script>alert(1)</script> title`}
	out := string(enrichHTMLForSharing(in, meta, ""))
	if strings.Contains(out, "<script>alert(1)") {
		t.Error("raw <script> from title leaked into og:title (must be HTML-escaped)")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Error("expected escaped title in output")
	}
}

func TestEnrichHTMLIdempotent(t *testing.T) {
	in := []byte("<html><head><title>Old</title></head><body>x</body></html>")
	meta := map[string]any{"title": "v1"}
	once := enrichHTMLForSharing(in, meta, "")
	// Re-enrich with different content must REPLACE the block, not append a second one.
	twice := enrichHTMLForSharing(once, map[string]any{"title": "v2"}, "")
	if got := strings.Count(string(twice), "makro-meta:start"); got != 1 {
		t.Errorf("expected exactly 1 makro-meta block after re-enrich, got %d", got)
	}
	if !strings.Contains(string(twice), `og:title" content="v2"`) || strings.Contains(string(twice), `og:title" content="v1"`) {
		t.Error("re-enrich did not refresh the block to the new title")
	}
}

func TestEnrichHTMLTitleFallsBackToTitleTag(t *testing.T) {
	in := []byte("<html><head><title>  Page Title  </title></head><body></body></html>")
	out := string(enrichHTMLForSharing(in, map[string]any{}, ""))
	if !strings.Contains(out, `og:title" content="Page Title"`) {
		t.Errorf("title fallback to <title> tag failed, got: %s", out)
	}
}

func TestEnrichHTMLNoHeadNoBodyLeftUntouched(t *testing.T) {
	in := []byte("<p>fragment only</p>")
	out := enrichHTMLForSharing(in, map[string]any{"title": "t"}, "")
	if string(out) != string(in) {
		t.Error("fragment without head/body must be returned unchanged")
	}
}

func TestWriteShareMetaMergesWithoutClobbering(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.meta.json")
	orig := map[string]any{"id": "abc", "title": "原报告", "tldr": "保留我"}
	b, _ := json.Marshal(orig)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeShareMeta(p, shareMeta{ShareHash: "h1", ShareKey: "h1/a.html", ShareMtime: 42, ShareURL: "https://s/x", SharedAt: "2026"}); err != nil {
		t.Fatalf("writeShareMeta: %v", err)
	}

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	// Pre-existing fields survive.
	for _, k := range []string{"id", "title", "tldr"} {
		if _, ok := got[k]; !ok {
			t.Errorf("field %q was clobbered by writeShareMeta", k)
		}
	}
	// share_* fields land.
	if got["share_hash"] != "h1" || got["share_mtime"] != float64(42) {
		t.Errorf("share fields not written: %+v", got)
	}
	// No temp file left behind by the atomic write.
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("stray tmp file: %v", err)
	}
}

func TestMetaPathFor(t *testing.T) {
	got := metaPathFor("/store/dev/report.v2.html")
	want := "/store/dev/report.v2.meta.json"
	if got != want {
		t.Errorf("metaPathFor = %q, want %q", got, want)
	}
}
