package apns

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"testing"
)

func TestPadFixed(t *testing.T) {
	want := []byte{0, 0, 0, 1}
	got := padFixed(big.NewInt(1), 4)
	if len(got) != len(want) {
		t.Fatalf("padFixed len = %d, want %d (% x)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("padFixed = % x, want % x", got, want)
		}
	}
}

func TestBuildPayload(t *testing.T) {
	data, err := buildPayload("Makro", "dev", "all good", "dev")
	if err != nil {
		t.Fatal(err)
	}
	// Round-trip back through a generic map to assert structure.
	got := decode(t, data)

	aps, ok := got["aps"].(map[string]any)
	if !ok {
		t.Fatalf("missing aps: %v", got)
	}
	if aps["sound"] != "default" {
		t.Errorf("sound = %v", aps["sound"])
	}
	if aps["category"] != "SESSION_DONE" {
		t.Errorf("category = %v", aps["category"])
	}
	alert := aps["alert"].(map[string]any)
	if alert["title"] != "Makro" || alert["subtitle"] != "dev" || alert["body"] != "all good" {
		t.Errorf("alert = %v", alert)
	}
	if got["session"] != "dev" {
		t.Errorf("session = %v", got["session"])
	}
}

func TestBuildPayloadOmitsEmpty(t *testing.T) {
	data, err := buildPayload("Makro", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, data)
	alert := got["aps"].(map[string]any)["alert"].(map[string]any)
	if _, has := alert["subtitle"]; has {
		t.Error("subtitle should be omitted when empty")
	}
	if _, has := alert["body"]; has {
		t.Error("body should be omitted when empty")
	}
}

func TestSignedJWT(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{
		key:    priv,
		keyID:  "ABC123KEY",
		teamID: "TEAM1234",
	}

	jwt, err := c.signedJWT(1700000000)
	if err != nil {
		t.Fatal(err)
	}

	parts := splitExactly(jwt, '.')
	if len(parts) != 3 {
		t.Fatalf("jwt has %d segments, want 3", len(parts))
	}

	header := decode(t, mustB64Decode(t, parts[0]))
	if header["alg"] != "ES256" || header["typ"] != "JWT" || header["kid"] != "ABC123KEY" {
		t.Errorf("header = %v", header)
	}
	claims := decode(t, mustB64Decode(t, parts[1]))
	if claims["iss"] != "TEAM1234" {
		t.Errorf("iss = %v", claims["iss"])
	}
	if claims["iat"] != 1700000000.0 {
		t.Errorf("iat = %v", claims["iat"])
	}

	// Signature: raw R‖S, 64 bytes for P-256, and must verify.
	sig := mustB64Decode(t, parts[2])
	rb := sig[:32]
	sb := sig[32:]
	r := new(big.Int).SetBytes(rb)
	s := new(big.Int).SetBytes(sb)
	signingInput := parts[0] + "." + parts[1]
	digest := sha256.Sum256([]byte(signingInput))
	if !ecdsa.Verify(&priv.PublicKey, digest[:], r, s) {
		t.Error("JWT signature does not verify")
	}
}

func TestNewClientRejectsMissing(t *testing.T) {
	if _, err := NewClient("", "k", "t", "b", true); err == nil {
		t.Error("expected error for empty keyPath")
	}
	if _, err := NewClient("/nonexistent.p8", "k", "t", "b", true); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestNewClientFromPEM(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	f, err := os.CreateTemp("", "authkey-*.p8")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(pemBytes); err != nil {
		t.Fatal(err)
	}
	f.Close()

	c, err := NewClient(f.Name(), "KID123", "TEAM456", "com.test.app", true)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c == nil || c.key == nil {
		t.Fatal("nil client/key")
	}
}

// --- helpers ---

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode %q: %v", b, err)
	}
	return out
}

func mustB64Decode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64 decode %q: %v", s, err)
	}
	return b
}

func splitExactly(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
