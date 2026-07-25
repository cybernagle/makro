package main

import (
	"fmt"
	"os"
	"testing"
)

// TestShareE2E exercises the real share path against the live OSS bucket
// (creds auto-read from ~/.aliyun/config.json). It uploads a real artifact,
// prints the presigned URL, and checks the memoize short-circuit on a second
// call. Gated behind MAKRO_SHARE_E2E=1 so routine `go test` doesn't hit OSS.
// Run: MAKRO_SHARE_E2E=1 go test ./cmd/gui/ -run TestShareE2E -v -count=1
func TestShareE2E(t *testing.T) {
	if os.Getenv("MAKRO_SHARE_E2E") != "1" {
		t.Skip("set MAKRO_SHARE_E2E=1 to run live OSS share e2e (uploads to real bucket)")
	}
	svc := NewShareService(&ArtifactService{})
	if svc.initErr != nil {
		t.Skipf("share disabled (expected if no creds): %v", svc.initErr)
	}

	const session, path = "dev", "share-component-how-it-works.html"

	res, err := svc.Share(session, path)
	if err != nil {
		t.Fatalf("Share: %v", err)
	}
	t.Logf("URL    : %s", res.URL)
	t.Logf("Hash   : %s", res.Hash)
	t.Logf("Cached : %v", res.Cached)
	fmt.Printf("\n>>> SHARE URL: %s\n\n", res.URL)

	// Second call must be memoized (no re-upload).
	res2, err := svc.Share(session, path)
	if err != nil {
		t.Fatalf("Share(2nd): %v", err)
	}
	t.Logf("Cached2: %v", res2.Cached)
	if !res2.Cached {
		t.Errorf("second share was not memoized (expected cached=true)")
	}
}
