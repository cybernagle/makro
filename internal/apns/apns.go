// Package apns sends push notifications to iOS devices via Apple Push
// Notification service (APNs), using HTTP/2 and token-based (JWT) auth.
//
// It is best-effort: push errors are returned to the caller, which should log
// them but never let a notification failure break the host application.
package apns

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"time"
)

const (
	hostProduction = "https://api.push.apple.com"
	hostSandbox    = "https://api.sandbox.push.apple.com"
)

// Client sends pushes to APNs for a fixed Auth Key + topic (bundle id).
type Client struct {
	httpClient *http.Client
	key        *ecdsa.PrivateKey
	keyID      string
	teamID     string
	topic      string // bundle id, used as apns-topic
	host       string
}

// NewClient loads an Apple APNs Auth Key (.p8) and returns a push client.
// bundleID is used as the apns-topic. sandbox selects the sandbox gateway
// (required while the app is signed with a development certificate).
func NewClient(keyPath, keyID, teamID, bundleID string, sandbox bool) (*Client, error) {
	if keyPath == "" || keyID == "" || teamID == "" || bundleID == "" {
		return nil, fmt.Errorf("apns: key_path, key_id, team_id and bundle_id are required")
	}
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("apns: read key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("apns: key is not valid PEM (expect -----BEGIN PRIVATE KEY-----)")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apns: parse key (expect PKCS#8 .p8): %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("apns: key is not ECDSA P-256")
	}
	host := hostProduction
	if sandbox {
		host = hostSandbox
	}
	return &Client{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		key:        key,
		keyID:      keyID,
		teamID:     teamID,
		topic:      bundleID,
		host:       host,
	}, nil
}

// Push sends an alert notification to deviceToken. session is carried as a
// custom payload key so the app can deep-link to that session on tap.
func (c *Client) Push(ctx context.Context, deviceToken, title, subtitle, body, session string) error {
	if deviceToken == "" {
		return fmt.Errorf("apns: empty device token")
	}
	jwt, err := c.signedJWT(time.Now().Unix())
	if err != nil {
		return fmt.Errorf("apns: sign jwt: %w", err)
	}

	payload, err := buildPayload(title, subtitle, body, session)
	if err != nil {
		return fmt.Errorf("apns: payload: %w", err)
	}

	url := c.host + "/3/device/" + deviceToken
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", c.topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("content-type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("apns: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("apns: %s: %s", resp.Status, string(respBody))
	}
	return nil
}

// signedJWT builds an ES256-signed provider JWT: header {alg,typ,kid},
// claims {iss,iat}, signature = raw R‖S over SHA-256 of the signing input.
// now is a unix timestamp (parameterized for testability).
func (c *Client) signedJWT(now int64) (string, error) {
	header := fmt.Sprintf(`{"alg":"ES256","typ":"JWT","kid":%q}`, c.keyID)
	claims := fmt.Sprintf(`{"iss":%q,"iat":%d}`, c.teamID, now)
	signingInput := b64([]byte(header)) + "." + b64([]byte(claims))

	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, c.key, digest[:])
	if err != nil {
		return "", err
	}
	curveBits := c.key.Curve.Params().BitSize
	byteLen := (curveBits + 7) / 8 // P-256 → 32
	sig := append(padFixed(r, byteLen), padFixed(s, byteLen)...)
	return signingInput + "." + b64(sig), nil
}

// buildPayload marshals the aps alert. mutable-content is 0 (no extension);
// the "session" custom key drives deep-linking.
func buildPayload(title, subtitle, body, session string) ([]byte, error) {
	alert := map[string]string{"title": title}
	if subtitle != "" {
		alert["subtitle"] = subtitle
	}
	if body != "" {
		alert["body"] = body
	}
	m := map[string]any{
		"aps": map[string]any{
			"alert": alert,
			"sound": "default",
			"badge": 1,
		},
		"session": session,
	}
	if title != "" {
		m["aps"].(map[string]any)["category"] = "SESSION_DONE"
	}
	return json.Marshal(m)
}

// padFixed left-zero-pads a big.Int to exactly n bytes.
func padFixed(b *big.Int, n int) []byte {
	out := make([]byte, n)
	b.FillBytes(out)
	return out
}

func b64(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
