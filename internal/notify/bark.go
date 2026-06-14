package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// BarkPush sends a push notification via the Bark app's HTTP API.
// The user installs Bark on their iPhone and provides their device key.
// serverURL defaults to "https://api.day.app" if empty.
func BarkPush(ctx context.Context, serverURL, key, title, subtitle, body string) error {
	if key == "" {
		return fmt.Errorf("bark: empty key")
	}
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if serverURL == "" {
		serverURL = "https://api.day.app"
	}

	payload, _ := json.Marshal(map[string]string{
		"title":    title,
		"subtitle": subtitle,
		"body":     truncateBody(body),
		"group":    "Makro",
		"sound":    "multiwayinvitation",
	})

	url := fmt.Sprintf("%s/%s", serverURL, key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("bark: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bark: HTTP %d", resp.StatusCode)
	}
	return nil
}
