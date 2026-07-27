package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

// ── Artifact sharing via Aliyun OSS ──
//
// iOS taps share → POST /api/artifact/share → this service:
//  1. Resolves the local artifact file (reuses ArtifactService.ResolveArtifact)
//  2. Memoize check: if the sibling <name>.meta.json already has share_key and
//     the file mtime is unchanged → re-sign a presigned URL (cheap, no re-upload)
//  3. Otherwise: new hash (crypto/rand 16B) → OSS PUT <hash>/<file>
//     (Content-Type text/html) → sign presigned GET → write share_* back into
//     meta.json (merged, not clobbered)
//
// The presigned URL is signed against the bucket endpoint, then the host is
// rewritten to ShareDomain (CNAME-bound to the bucket). OSS V1 signature is
// over the resource path + expires, not the host, so the rewrite stays valid.
//
// OSS creds come from MAKRO_OSS_* env (AK/SK must not live in iOS). The Mac
// must be running for iOS to share — that's the single runtime precondition.

// ShareConfig is the OSS config for artifact sharing, read from MAKRO_OSS_* env.
type ShareConfig struct {
	AccessKey   string
	SecretKey   string
	Bucket      string
	Region      string
	ShareDomain string // share.juliasia.cn (CNAME → bucket endpoint)
	ShareHost   string // SSH host alias for the card-landing server (e.g. "julia")
	SharePath   string // dir on ShareHost to drop landing pages (e.g. /home/juli/web/s)
	CardDomain  string // public domain serving the landings (e.g. juliasia.cn)
}

func loadShareConfig() ShareConfig {
	cfg := ShareConfig{
		Bucket:      shareEnvOr("MAKRO_OSS_BUCKET", "juli-makro"),
		ShareDomain: shareEnvOr("MAKRO_OSS_SHARE_DOMAIN", "share.juliasia.cn"),
		ShareHost:   shareEnvOr("MAKRO_SHARE_HOST", "julia"),
		SharePath:   shareEnvOr("MAKRO_SHARE_PATH", "/home/juli/web/s"),
		CardDomain:  shareEnvOr("MAKRO_SHARE_CARD_DOMAIN", "juliasia.cn"),
	}
	// Credentials: env first, then fall back to the aliyun CLI profile so
	// sharing works with zero extra setup (creds already in ~/.aliyun/config.json
	// from `aliyun configure` / infra apply).
	cfg.AccessKey = os.Getenv("MAKRO_OSS_ACCESS_KEY")
	cfg.SecretKey = os.Getenv("MAKRO_OSS_SECRET_KEY")
	if os.Getenv("MAKRO_OSS_REGION") != "" {
		cfg.Region = os.Getenv("MAKRO_OSS_REGION")
	} else {
		cfg.Region = "cn-hangzhou"
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		if ak, sk, region, err := loadCredsFromAliyunProfile(); err == nil {
			if cfg.AccessKey == "" {
				cfg.AccessKey = ak
			}
			if cfg.SecretKey == "" {
				cfg.SecretKey = sk
			}
			if os.Getenv("MAKRO_OSS_REGION") == "" && region != "" {
				cfg.Region = region
			}
		}
	}
	return cfg
}

// aliyunProfileConfig matches ~/.aliyun/config.json (aliyun CLI v3 shape).
type aliyunProfileConfig struct {
	Current  string `json:"current"`
	Profiles []struct {
		Name            string `json:"name"`
		Mode            string `json:"mode"`
		RegionID        string `json:"region_id"`
		AccessKeyID     string `json:"access_key_id"`
		AccessKeySecret string `json:"access_key_secret"`
	} `json:"profiles"`
}

// loadCredsFromAliyunProfile reads the aliyun CLI config (~/.aliyun/config.json,
// fallback ~/.alicloud/config.json) and returns the AK/SK of the current (or
// "default") AK-mode profile, plus its region.
func loadCredsFromAliyunProfile() (ak, sk, region string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", "", err
	}
	var data []byte
	for _, p := range []string{".aliyun/config.json", ".alicloud/config.json"} {
		if b, e := os.ReadFile(filepath.Join(home, p)); e == nil {
			data = b
			break
		}
	}
	if data == nil {
		return "", "", "", fmt.Errorf("no aliyun config (~/.aliyun/config.json)")
	}
	var cfg aliyunProfileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", "", "", fmt.Errorf("parse aliyun config: %w", err)
	}
	want := cfg.Current
	if want == "" {
		want = "default"
	}
	// Prefer the current/default AK-mode profile.
	for _, p := range cfg.Profiles {
		if p.Name == want && (p.Mode == "AK" || p.Mode == "") && p.AccessKeyID != "" && p.AccessKeySecret != "" {
			return p.AccessKeyID, p.AccessKeySecret, p.RegionID, nil
		}
	}
	// Else any AK-mode profile.
	for _, p := range cfg.Profiles {
		if (p.Mode == "AK" || p.Mode == "") && p.AccessKeyID != "" && p.AccessKeySecret != "" {
			return p.AccessKeyID, p.AccessKeySecret, p.RegionID, nil
		}
	}
	return "", "", "", fmt.Errorf("no AK-mode profile in aliyun config")
}

func shareEnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ShareService uploads artifacts to OSS and signs presigned share URLs.
type ShareService struct {
	cfg        ShareConfig
	arts       *ArtifactService
	bucket     *oss.Bucket
	ogImageURL string // presigned URL of the brand OG card (assets/makro-og.png)
	initErr    error
}

// NewShareService reads OSS env and connects. If AK/SK are unset the service is
// returned with initErr set (handlers report sharing-disabled, server still runs).
func NewShareService(arts *ArtifactService) *ShareService {
	s := &ShareService{cfg: loadShareConfig(), arts: arts}
	if s.cfg.AccessKey == "" || s.cfg.SecretKey == "" {
		s.initErr = fmt.Errorf("OSS creds not found: set MAKRO_OSS_ACCESS_KEY/MAKRO_OSS_SECRET_KEY or run `aliyun configure` (~/.aliyun/config.json)")
		return s
	}
	endpoint := fmt.Sprintf("oss-%s.aliyuncs.com", s.cfg.Region)
	client, err := oss.New(endpoint, s.cfg.AccessKey, s.cfg.SecretKey)
	if err != nil {
		s.initErr = fmt.Errorf("oss client: %w", err)
		return s
	}
	bucket, err := client.Bucket(s.cfg.Bucket)
	if err != nil {
		s.initErr = fmt.Errorf("oss bucket: %w", err)
		return s
	}
	s.bucket = bucket
	// Brand OG card image. MUST be a clean, public, SAME-DOWNLOAD-DOMAIN URL —
	// WeChat's card crawler can't fetch a presigned / cross-domain og:image (it
	// hits share.juliasia.cn without the signature → OSS AccessDenied → the whole
	// card is rejected blank). Served publicly from julia ECS nginx.
	s.ogImageURL = shareEnvOr("MAKRO_OG_IMAGE", "https://juliasia.cn/og/makro-og.png")
	return s
}

// ShareResult is the response shape for POST /api/artifact/share.
type ShareResult struct {
	URL    string `json:"url"`
	Hash   string `json:"hash"`
	Cached bool   `json:"cached"`
}

// Share uploads the artifact (if first share or content changed) and returns a
// presigned GET URL on the ShareDomain. Memoized per artifact version via the
// sibling <name>.meta.json.
func (s *ShareService) Share(session, relPath string) (*ShareResult, error) {
	if s.initErr != nil {
		return nil, s.initErr
	}
	absPath, _, err := s.arts.ResolveArtifact(session, relPath)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("stat artifact: %w", err)
	}
	mtime := fi.ModTime().Unix()
	filename := filepath.Base(absPath)
	metaPath := metaPathFor(absPath)

	// Memoized + unchanged → return the CLEAN card URL (https://<CardDomain>/s/<hash>),
	// reconstructed from share_hash. This upgrades OLD shares (whose stored
	// share_url is a presigned share.juliasia.cn URL that WeChat won't card) to
	// the clean card URL. The landing is published by Share()/ReenrichAll().
	if meta, _ := readShareMeta(metaPath); meta.ShareKey != "" && meta.ShareMtime == mtime && meta.ShareHash != "" {
		return &ShareResult{URL: fmt.Sprintf("https://%s/share-%s.html", s.cfg.CardDomain, meta.ShareHash), Hash: meta.ShareHash, Cached: true}, nil
	}

	// New or changed → fresh hash + upload.
	hash, err := newShareHash()
	if err != nil {
		return nil, err
	}
	key := hash + "/" + filename
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read artifact: %w", err)
	}
	// Enrich the published copy with provenance metadata (description/author/
	// OpenGraph/JSON-LD) pulled from meta.json, so AI tools / crawlers / link
	// previews can attribute the report (fixes "recipient AI doesn't know where
	// this came from"). Idempotent — refreshes the makro-meta block on re-upload.
	fullMeta := readFullMetaMap(metaPath)
	enriched := enrichHTMLForSharing(data, fullMeta, s.ogImageURL)
	if err := s.bucket.PutObject(key, bytes.NewReader(enriched), oss.ContentType("text/html; charset=utf-8")); err != nil {
		return nil, fmt.Errorf("oss put: %w", err)
	}
	url, err := s.signURL(key)
	if err != nil {
		return nil, fmt.Errorf("sign url: %w", err)
	}

	// Serve the FULL enriched report at the clean card URL (https://<CardDomain>/s/<hash>).
	// WeChat doesn't card thin/redirect preview pages (anti-spam) — it cards RICH
	// pages like the main site. So the card URL serves the report itself (with the
	// provenance OG in <head>). ECS stores 1 copy (~tens of KB) per share.
	cardURL := fmt.Sprintf("https://%s/share-%s.html", s.cfg.CardDomain, hash)
	if err := s.publishLanding(hash, string(enriched)); err != nil {
		log.Printf("[share] landing publish failed (%v) — returning presigned URL (no card)", err)
	} else {
		url = cardURL
	}

	if err := writeShareMeta(metaPath, shareMeta{
		ShareHash: hash, ShareKey: key, ShareMtime: mtime, ShareURL: url,
		SharedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		// Non-fatal: the share itself succeeded; memoize just won't work next time.
		log.Printf("[share] write meta %s: %v", metaPath, err)
	}
	log.Printf("[share] uploaded %s/%s → %s (hash %s)", session, filename, s.cfg.ShareDomain, hash)
	return &ShareResult{URL: url, Hash: hash, Cached: false}, nil
}

// signURL signs a presigned GET for the artifact content. Long-lived (10y) so
// the card-landing's redirect target (and the iOS fallback URL) stay valid.
func (s *ShareService) signURL(key string) (string, error) {
	return s.signURLExpiry(key, 10*365*24*time.Hour)
}

// signURLExpiry signs a presigned GET for a custom lifetime, then rewrites the
// host to the ShareDomain (CNAME-bound to the bucket) and forces https (OSS V1
// signature is scheme-agnostic, and the LE cert is bound to the cname).
func (s *ShareService) signURLExpiry(key string, expiry time.Duration) (string, error) {
	raw, err := s.bucket.SignURL(key, oss.HTTPGet, int64(expiry.Seconds()))
	if err != nil {
		return "", err
	}
	ossHost := fmt.Sprintf("%s.oss-%s.aliyuncs.com", s.cfg.Bucket, s.cfg.Region)
	out := strings.Replace(raw, ossHost, s.cfg.ShareDomain, 1)
	out = strings.Replace(out, "http://"+s.cfg.ShareDomain, "https://"+s.cfg.ShareDomain, 1)
	return out, nil
}

func newShareHash() (string, error) {
	b := make([]byte, 16) // 128 bit → 32 hex chars
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// buildShareLanding renders the card-landing HTML served at https://<CardDomain>/s/<hash>.
// Carries the provenance OG (so WeChat/link-preview crawlers card the CLEAN url) +
// a "view report" button to the presigned content. NO auto-redirect (meta-refresh
// makes WeChat follow/skip and not card the page) — the user taps the button.
func buildShareLanding(meta map[string]any, contentURL, ogImageURL, cardURL string) string {
	title, _ := meta["title"].(string)
	if title == "" {
		title = "Makro Artifact"
	}
	desc, _ := meta["tldr"].(string)
	e := html.EscapeString
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html lang=\"zh-CN\"><head><meta charset=\"UTF-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n")
	fmt.Fprintf(&b, "<title>%s · 橘粒 Juli</title>\n", e(title))
	fmt.Fprintf(&b, "<meta name=\"description\" content=\"%s\">\n", e(desc))
	b.WriteString("<meta name=\"author\" content=\"橘粒 Juli\">\n")
	fmt.Fprintf(&b, "<meta property=\"og:title\" content=\"%s · 橘粒 Juli\">\n", e(title))
	fmt.Fprintf(&b, "<meta property=\"og:description\" content=\"%s\">\n", e(desc))
	b.WriteString("<meta property=\"og:type\" content=\"article\">\n")
	b.WriteString("<meta property=\"og:site_name\" content=\"橘粒 Juli\">\n")
	fmt.Fprintf(&b, "<meta property=\"og:url\" content=\"%s\">\n", e(cardURL))
	if ogImageURL != "" {
		fmt.Fprintf(&b, "<meta property=\"og:image\" content=\"%s\">\n", e(ogImageURL))
		b.WriteString("<meta property=\"og:image:width\" content=\"1200\">\n<meta property=\"og:image:height\" content=\"630\">\n")
		b.WriteString("<meta name=\"twitter:card\" content=\"summary_large_image\">\n")
		fmt.Fprintf(&b, "<meta name=\"twitter:image\" content=\"%s\">\n", e(ogImageURL))
	}
	b.WriteString("<style>:root{--c:#D97C26;--c-deep:#B8681A;--ink:#1A1A1A;--body:#5C5A54;--dim:#8E8C84;--bg:#FAFAF8}*{margin:0;padding:0;box-sizing:border-box}body{font-family:-apple-system,'PingFang SC',sans-serif;background:var(--bg);color:var(--ink);display:flex;align-items:center;justify-content:center;min-height:100vh;padding:32px}.c{max-width:520px;text-align:center}.k{font-family:'SF Mono',Menlo,monospace;font-size:11px;color:var(--c);letter-spacing:2px;margin-bottom:14px}h1{font-size:22px;font-weight:700;letter-spacing:-.4px;line-height:1.35;margin-bottom:14px}.d{font-size:14px;color:var(--body);line-height:1.7;margin-bottom:28px}.btn{display:inline-block;padding:13px 30px;background:var(--c);color:#fff;text-decoration:none;border-radius:8px;font-size:15px;font-weight:500;box-shadow:0 4px 14px -4px rgba(217,124,38,.4);transition:background .25s}.btn:hover{background:var(--c-deep)}.f{margin-top:28px;font-size:12px;color:var(--dim)}</style>\n")
	b.WriteString("</head>\n<body><div class=\"c\">")
	b.WriteString("<div class=\"k\">// MAKRO ARTIFACT</div>")
	fmt.Fprintf(&b, "<h1>%s</h1>", e(title))
	fmt.Fprintf(&b, "<p class=\"d\">%s</p>", e(desc))
	fmt.Fprintf(&b, "<a class=\"btn\" href=\"%s\">查看完整报告 →</a>", e(contentURL))
	b.WriteString("<div class=\"f\">橘粒 Juli · 由 Makro™ 生成</div>")
	b.WriteString("</div></body></html>\n")
	return b.String()
}

// publishLanding writes the card-landing HTML to <ShareHost>:<SharePath>/<hash>.html
// over SSH (stdin → cat), chmod 644 so nginx can serve it. Idempotent (overwrites).
func (s *ShareService) publishLanding(hash, landingHTML string) error {
	if s.cfg.ShareHost == "" {
		return fmt.Errorf("ShareHost empty (set MAKRO_SHARE_HOST)")
	}
	p := s.cfg.SharePath
	cmd := exec.Command("ssh", s.cfg.ShareHost,
		fmt.Sprintf("mkdir -p %s && cat > %s/%s.html && chmod 644 %s/%s.html", p, p, hash, p, hash))
	cmd.Stdin = strings.NewReader(landingHTML)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh %s: %w: %s", s.cfg.ShareHost, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ── meta.json (sibling file) memoize fields ──
// The artifact <name>.html has a sibling <name>.meta.json (makro-artifacts
// convention). We add share_* fields to it WITHOUT clobbering id/title/tldr/etc.

type shareMeta struct {
	ShareHash  string `json:"share_hash,omitempty"`
	ShareKey   string `json:"share_key,omitempty"`
	ShareMtime int64  `json:"share_mtime,omitempty"`
	ShareURL   string `json:"share_url,omitempty"`
	SharedAt   string `json:"shared_at,omitempty"`
}

func metaPathFor(artifactAbs string) string {
	dir := filepath.Dir(artifactAbs)
	ext := filepath.Ext(artifactAbs)
	base := strings.TrimSuffix(filepath.Base(artifactAbs), ext)
	return filepath.Join(dir, base+".meta.json")
}

func readShareMeta(path string) (shareMeta, error) {
	var m shareMeta
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	_ = json.Unmarshal(data, &m) // picks share_* fields, ignores the rest
	return m, nil
}

// writeShareMeta merges the share_* fields into the existing meta.json without
// clobbering the rest.
func writeShareMeta(path string, sm shareMeta) error {
	full := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &full)
	}
	if sm.ShareHash != "" {
		full["share_hash"] = sm.ShareHash
	}
	if sm.ShareKey != "" {
		full["share_key"] = sm.ShareKey
	}
	full["share_mtime"] = sm.ShareMtime
	if sm.ShareURL != "" {
		full["share_url"] = sm.ShareURL
	}
	if sm.SharedAt != "" {
		full["shared_at"] = sm.SharedAt
	}
	b, err := json.MarshalIndent(full, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// readFullMetaMap reads the whole meta.json into a map (empty if absent). Used
// to pull title/tldr/created/tags for the published-copy enrichment.
func readFullMetaMap(path string) map[string]any {
	m := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

// enrichHTMLForSharing injects provenance metadata (meta description/author,
// OpenGraph, JSON-LD TechArticle) into the HTML <head>, sourced from meta.json.
// Idempotent via the makro-meta markers. This makes the published page
// self-describing for AI tools / crawlers / link previews.
// titleTagRe extracts the page's <title> for artifacts whose meta.json lacks one.
var titleTagRe = regexp.MustCompile(`(?i)<title>([^<]+)</title>`)

func enrichHTMLForSharing(htmlBytes []byte, meta map[string]any, ogImageURL string) []byte {
	s := string(htmlBytes)
	title, _ := meta["title"].(string)
	if title == "" {
		// fall back to the page's <title> tag (old artifacts without a meta.json title)
		if m := titleTagRe.FindStringSubmatch(s); len(m) > 1 {
			title = strings.TrimSpace(m[1])
		}
	}
	if title == "" {
		title = "Makro Artifact · 橘粒 Juli"
	}
	desc, _ := meta["tldr"].(string)
	created, _ := meta["created"].(string)

	var kws []string
	if tags, ok := meta["tags"].([]any); ok {
		for _, t := range tags {
			if ts, ok := t.(string); ok {
				kws = append(kws, ts)
			}
		}
	}
	keywords := strings.Join(kws, ", ")

	// JSON-LD: json.Marshal HTML-escapes by default → safe inside <script>.
	ld := map[string]any{
		"@context":      "https://schema.org",
		"@type":         "TechArticle",
		"headline":      title,
		"description":   desc,
		"datePublished": created,
		"author":        map[string]any{"@type": "Organization", "name": "橘粒 Juli", "url": "https://juliasia.cn"},
		"publisher":     map[string]any{"@type": "Organization", "name": "上海橘粒科技有限公司", "alternateName": "橘粒 Juli"},
	}
	if keywords != "" {
		ld["keywords"] = keywords
	}
	ldJSON, _ := json.Marshal(ld)

	e := html.EscapeString
	block := "<!-- makro-meta:start -->\n"
	block += fmt.Sprintf(`<meta name="description" content="%s">`+"\n", e(desc))
	block += `<meta name="author" content="橘粒 Juli">` + "\n"
	block += `<meta name="generator" content="Makro (https://juliasia.cn)">` + "\n"
	block += fmt.Sprintf(`<meta property="og:title" content="%s">`+"\n", e(title))
	block += fmt.Sprintf(`<meta property="og:description" content="%s">`+"\n", e(desc))
	block += `<meta property="og:type" content="article">` + "\n"
	block += `<meta property="og:site_name" content="橘粒 Juli">` + "\n"
	if ogImageURL != "" {
		block += fmt.Sprintf(`<meta property="og:image" content="%s">`+"\n", e(ogImageURL))
		block += `<meta property="og:image:width" content="1200">` + "\n"
		block += `<meta property="og:image:height" content="630">` + "\n"
		block += `<meta name="twitter:card" content="summary_large_image">` + "\n"
		block += fmt.Sprintf(`<meta name="twitter:image" content="%s">`+"\n", e(ogImageURL))
	}
	if keywords != "" {
		block += fmt.Sprintf(`<meta name="keywords" content="%s">`+"\n", e(keywords))
	}
	block += `<script type="application/ld+json">` + string(ldJSON) + `</script>` + "\n"
	block += "<!-- makro-meta:end -->"

	// Idempotent: refresh an existing makro-meta block; else inject before </head>.
	if idx := strings.Index(s, "<!-- makro-meta:start -->"); idx >= 0 {
		endMarker := "<!-- makro-meta:end -->"
		if end := strings.Index(s[idx:], endMarker); end >= 0 {
			endPos := idx + end + len(endMarker)
			return []byte(s[:idx] + block + s[endPos:])
		}
	}
	if i := strings.Index(strings.ToLower(s), "</head>"); i >= 0 {
		return []byte(s[:i] + block + "\n" + s[i:])
	}
	if i := strings.Index(strings.ToLower(s), "<body"); i >= 0 {
		return []byte(s[:i] + block + "\n" + s[i:])
	}
	return htmlBytes // no head/body marker — leave untouched rather than break the page
}

// ReenrichAll re-uploads the enriched (provenance-meta-injected) HTML to every
// already-shared artifact's existing share_key, so OLD share URLs pick up the
// metadata without changing URL. One-off backfill. Returns (updated, skipped).
func (s *ShareService) ReenrichAll() (int, int, error) {
	if s.initErr != nil {
		return 0, 0, s.initErr
	}
	root := centralArtifactsRoot()
	updated, skipped := 0, 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".meta.json") {
			return nil
		}
		full := readFullMetaMap(path)
		key, _ := full["share_key"].(string)
		if key == "" {
			return nil // not shared
		}
		// local html = meta.json's dir + the filename part of share_key
		filename := key
		if i := strings.Index(key, "/"); i >= 0 {
			filename = key[i+1:]
		}
		htmlPath := filepath.Join(filepath.Dir(path), filename)
		data, err := os.ReadFile(htmlPath)
		if err != nil {
			log.Printf("[reenrich] skip %s: %v", key, err)
			skipped++
			return nil
		}
		enriched := enrichHTMLForSharing(data, full, s.ogImageURL)
		if err := s.bucket.PutObject(key, bytes.NewReader(enriched), oss.ContentType("text/html; charset=utf-8")); err != nil {
			log.Printf("[reenrich] fail %s: %v", key, err)
			skipped++
			return nil
		}
		// also re-publish the card URL with the full enriched report (rich page →
		// WeChat cards it; thin preview pages don't get carded)
		hash := key
		if i := strings.Index(key, "/"); i >= 0 {
			hash = key[:i]
		}
		if err := s.publishLanding(hash, string(enriched)); err != nil {
			log.Printf("[reenrich] landing %s: %v", hash, err)
		}
		updated++
		log.Printf("[reenrich] ok %s", key)
		return nil
	})
	return updated, skipped, err
}

// ── HTTP handler ──

// artifactShareHandler uploads + shares an artifact.
// POST /api/artifact/share?session=<name>&path=<rel> → {url, hash, cached}.
func artifactShareHandler(svc *ShareService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Drain+close body (POST may be empty); keep the artifact identity in the query.
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		session := r.URL.Query().Get("session")
		relPath := r.URL.Query().Get("path")
		if session == "" || relPath == "" {
			http.Error(w, "session and path query params required", http.StatusBadRequest)
			return
		}
		res, err := svc.Share(session, relPath)
		if err != nil {
			log.Printf("[share] %s/%s: %v", session, relPath, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	}
}
