# iOS Push 调试最终状态（2026-06-14）

## 确诊（通过 codesign 对照实验）
- manual codesign **无 aps**（只 application-identifier/team/get-task-allow）→ installd ✅ 接受
- manual codesign **有 keychain-access-groups，无 aps** → installd ✅ 接受（"App installed"）
- manual codesign **有 aps-environment** → installd ❌ 拒（0xe8008015）
→ **aps-environment 是唯一被拒的 entitlement**。installd 对 aps 做 online portal Push 验证。

## profile/cert/device 全部正确
- profile 0e61f809 aps=development ✅（Mac + device 都验证过）
- cert C1FF3F7 在 profile DeveloperCertificates ✅
- device 00008140 在 profile ProvisionedDevices ✅
- entitlements 完整 ✅

## 唯一根因：portal App ID Push 缺 Development SSL Certificate
你之前问 SSL cert 要不要 create，我说不用（因为后端用 .p8）—— 但 installd 装 aps app 时做 online 验证，需要 portal Push **完全配置**（含 Development SSL Certificate）。

## 明天 3 步搞定
1. portal → identifiers → com.cybernagle.Makro → Push Notifications → **Configure** → Development → **Create Certificate** → 上传 `/tmp/MakroPush.csr` → 下载 .cer
2. CSR 我已生成好：`/tmp/MakroPush.csr`（+ `/tmp/makro_push.key`）
3. 重测：
```bash
APP=$(ls -dt ~/Library/Developer/Xcode/DerivedData/Makro-*/Build/Products/Debug-iphoneos/Makro.app | head -1)
codesign -f --entitlements /tmp/full.entitlements --sign C1FF3F7CC5FD1E01CD784CABE3159BB3CB48FC07 --generate-entitlement-der --timestamp=none "$APP"
xcrun devicectl device install app --device 4F46D7D1-16DC-58AB-85F2-C3242AE94E80 "$APP"
```
如果 install 成功 → 启动 app → push 注册 → 我触发推送验证。

## 后端已就绪
makro config (.p8 key 5298A7R98H) + internal/apns + device-token endpoint + OnAgentStop push 全部完成。
后端用 .p8（token-based），portal SSL cert 只是满足 installd 的安装验证。
