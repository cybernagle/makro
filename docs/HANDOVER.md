# Makro Handover Log

持续累积的交接记录,最新的在最上面。每条:做了什么 / 架构 / 关键决策与原因 / 怎么运维 / 当前状态 / 待办。给下一个 session 或人快速接手用。

---

## 2026-07-25 · share.juliasia.cn HTTPS(Let's Encrypt · 自动续签)

**状态:✅ 上线 + 验证 + 自动续签闭环**

给 07-24 的分享功能补 HTTPS —— share.juliasia.cn 之前 HTTP(微信/浏览器不友好),现在 LE 证书 + 自动续。

### 为什么 acme.sh(不是阿里云免费 DV / certbot)
- **阿里云免费 DV**:签发+续期 TF 管不了(只能管绑定),每 3 个月手动续 = 永远的税
- **certbot**:julia 服务器已有 certbot+nginx 体系(见 julia repo handover §21),但 share.juliasia.cn 是 **OSS 托管**(CNAME→OSS),certbot 的 nginx HTTP-01 不适用(请求不经 ECS nginx)
- **acme.sh + dns_ali(DNS-01)**:阿里云 DNS API 写 TXT 验证,不依赖域名指向;dns_ali 是阿里云 DNS-01 最成熟路径(**求稳**)

### 实现
| 项 | 值 |
|---|---|
| ACME 客户端 | acme.sh v3.1.5(装 Mac) |
| 验证 | dns_ali(凭证从 ~/.aliyun/config.json 取) |
| 证书 | LE/ZeroSSL ECC,90 天,2026-07-24 → 10-22 |
| 部署 | TF `alicloud_oss_bucket_cname.makro_share` 的 `certificate` 块,`file(pathexpand("~/.acme.sh/.../fullchain.cer"))` |
| 自动续 | acme.sh cron(每天 4 次)→ ARI 窗口续 → `--reloadcmd "terraform apply -target=cname"` → 新证书传 OSS |
| Go 链接 | `share_service.go` signURL 强制 https(OSS V1 签名 scheme-agnostic,换 https 仍有效) |

### 文件改动
- `infra/makro.tf` —— cname 加 `certificate { certificate=..., private_key=... }`
- `cmd/gui/share_service.go` —— signURL 强制 https
- `~/.acme.sh/` —— acme.sh + share.juliasia.cn 证书 + reloadcmd

### 验证
- `curl https://share.juliasia.cn/<presigned>` → HTTP 200,issuer=ZeroSSL ECC DV,CN=share.juliasia.cn ✅
- 新分享返回 `https://share.juliasia.cn/...`(cached: True)✅
- reloadcmd 注册成功(acme.sh conf base64),cron 装好 ✅

### 续签链路(不用管)
acme.sh cron 每天 → 到期前(ARI ~10-08)续 → 重写 PEM → reloadcmd(`terraform apply -target=cname`)→ OSS 拿新证书。**前提:Mac 偶尔开着**(60 天窗口内 Mac 醒着就续,可靠)。

---

## 2026-07-24 · Artifact 分享到 juliasia.cn(阿里云 OSS)

**状态:✅ 已上线 + iPhone 真机验证通过**

### 一句话

iPhone 点 artifact 的分享按钮 → Mac 后端上传到阿里云 OSS → 返回 `https://share.juliasia.cn/<hash>/<file>?Signature=...` presigned 链接 → 系统分享面板发出去。收件人打开链接看报告。

### 架构(两 repo 三层)

```
iPhone [↗ 分享]
  ↓ POST /api/artifact/share?session=&path=   (auth token)
Makro Mac 后端 (cmd/gui, Go)
  1. ResolveArtifact → 本地 ~/.makro/artifacts/<session>/<file>
  2. memoize: meta.json 有 share_key 且 mtime 没变 → 直接重签 URL(秒回)
  3. 否则: crypto/rand 16B hash → OSS PUT juli-makro/<hash>/<file> (text/html)
  4. 签 presigned GET → host 改写成 share.juliasia.cn → 回写 meta.json
  ↓ 返回 {url, hash, cached}
iPhone ShareLink → 微信/邮件/复制
```

收件人访问 `share.juliasia.cn` → DNS CNAME → OSS 桶直出(CDN 未上)。**juliasia.cn 服务器不在数据路径。**

### 文件改动

| repo | 文件 | 说明 |
|---|---|---|
| **Makro** | `cmd/gui/share_service.go`(新) | OSS 上传 + presign + memoize(比 mtime)+ hash + meta 合并 |
| Makro | `cmd/gui/server.go` | 加路由 `POST /api/artifact/share` |
| Makro | `cmd/gui/share_e2e_test.go`(新) | 后端 e2e 测试(gated by creds,无凭证 skip) |
| Makro | `ios/.../APIClient.swift` | `shareArtifact()` + `ShareResult` |
| Makro | `ios/.../ArtifactPreviewView.swift` | 工具栏分享按钮 + UIActivityViewController + 错误 alert |
| Makro | `go.mod` | + `github.com/aliyun/aliyun-oss-go-sdk v3.0.2` |
| **julia** | `infra/makro.tf`(新) | `alicloud_oss_bucket.makro`(私有)+ cname token + DNS CNAME/TXT + `oss_bucket_cname` 绑定 |
| julia | `infra/ram.tf` | 顺手对齐 `password_reset_required=false`(消 plan 噪声) |
| 全局 | `~/.claude/skills/makro-artifacts/SKILL.md` | 文档 share_* 为后端托管字段(agent 别覆盖) |

### 关键决策 + 原因(都是被现实逼的)

1. **private 桶 + presigned,不是 public-read** —— 账号开了「阻止公共访问」(Block Public Access),public-read ACL 被 OSS 拒(0015-00000501)。改私有 + 签名 URL,尊重安全策略。
2. **OSS CNAME 暴露 share.juliasia.cn,不是 CDN** —— 账号**没开通 CDN 服务**(`CdnServiceNotFound`)。改走 OSS 自定义域名绑定(CNAME 到桶 + 所有权 TXT 验证 + `alicloud_oss_bucket_cname`)。
3. **凭证 env → `~/.aliyun/config.json` fallback** —— Mac 后端读 OSS AK/SK:`MAKRO_OSS_*` env 优先,没设就读 aliyun CLI profile(零配置,infra apply 用的那套)。
4. **memoize 比对 mtime** —— 内容没变不重传,只重签 URL(1 年长效)。meta.json 存 `share_hash`/`share_key`/`share_mtime`/`share_url`/`shared_at`,合并不覆盖。
5. **桶 `juli-makro`,region `cn-hangzhou`**(跟 infra 一致)。

### 怎么运维

- **改了 Go 后端** → `cd Makro && go build -o cmd/gui/bin/makro-serve ./cmd/gui/` → 替换 `/Applications/Makro.app/Contents/Resources/bin/makro-serve` → `pkill -9 -f makro-serve && open /Applications/Makro.app`。前端没动可走外科替换,不必 electron-builder 重打包。
- **改了 iOS** → `cd ios/Makro && xcodegen generate && pod install` → Xcode build+装(Azure Speech pod 的模块解析裸 xcodebuild 有坑,用 Xcode GUI)。
- **分享不工作先查**:① Mac 开着没 ② `~/.aliyun/config.json` 在不在(或 `MAKRO_OSS_*` env)③ 桶/域名在不在(`dig share.juliasia.cn` 应 CNAME 到桶)。
- **TF 改动** → `cd ~/Desktop/Code/julia/infra && terraform plan/apply`(注意 makro 桶已 apply 过)。

### 验证过的事实

- 后端单测:`go test ./cmd/gui/ -run TestShareE2E -v`(真上传 + memoize)✅
- 部署端点:`POST /api/artifact/share` → 返链接 → curl HTTP 200 ✅
- iPhone 真机:用户验证分享 + 安装 ✅
- meta.json share_* 合并不破坏其他字段 ✅

### 待办 / 已知尾巴(都不阻塞)

- ~~`share.juliasia.cn` HTTP~~ → ✅ **HTTPS 已完成(2026-07-25)**,见上面那条 handover。acme.sh + LE,自动续签。
- **凭证偏宽** —— 现在读的是 `~/.aliyun/config.json` 里那套(基础设施级)。生产建议换只对 `juli-makro` 有写权限的受限 RAM key,用 `MAKRO_OSS_ACCESS_KEY/SECRET_KEY` env 覆盖。
- **CDN 未上** —— 现在 OSS 直出。若分享量起来 / 怕 DDoS 费用放大,再开通 CDN + 配私有桶回源鉴权。
- **`artifact-sharing-oss-decision` 决策 artifact 的 HTML body 还是旧方案文字**(public-read/CDN);meta.json 已同步成 private+presigned。要彻底一致需重渲染那份 HTML。
- **presigned URL 里 key 的 `/` 被 encode 成 `%2F`** —— OSS 正常处理,链接能用;URL 略长,无害。

### 关键约束(别踩)

- iOS app 用 `loadHTMLString(baseURL: nil)` → artifact 的 HTML 必须**全自包含**(不能引外部 JS/CSS/相对路径)。`makro-artifacts` skill 已强制。
- **Mac 必须开着**才能分享(OSS key 在 Mac 上,部署走 Mac 后端)。要 Mac-less 得改"创建即推 OSS"或云函数,动静大。
- 桶私有 → 只有 presigned 链接能取;匿名直接访问 OSS 会 403(正常)。
