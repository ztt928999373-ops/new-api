# G1 只读验收工具

`tools/g1/Invoke-G1Acceptance.ps1` 面向 PowerShell 7，只采集 loopback 网关状态、监听进程身份、已有 Mock NDJSON、正式 Bearer API 的用户/Key 元数据与消费日志。不会发送模型请求、访问 Mock `/healthz`、执行 `ops check` 或修改业务数据。

## 安全边界

- 只接受字面 IP loopback：`127.0.0.1` 或 `[::1]`。仅 HTTP(S) origin、端口必须匹配；拒绝主机名、URI 凭据、路径、query、fragment 和 HTTP 重定向。HTTP 客户端禁用自动重定向及代理。
- 监听验证使用 Windows `Get-NetTCPConnection`，每个监听地址必须为 loopback。提供已核验 EXE SHA-256 时，比较每个监听 PID 的映像哈希；不能核验的平台或环境返回 `UNKNOWN`。
- 认证只接受内存 `SecureString` Bearer。不要传 Cookie、WebRequestSession 或浏览器会话。为了构造 HTTP Authorization header，进程内会短暂形成 .NET 字符串；工具不会打印、落盘或导出该字符串。使用后应退出该 PowerShell 进程。
- 用户白名单字段：`id, username, quota, used_quota, request_count, status, role`。Key 白名单字段：`id, user_id, status, remain_quota, used_quota, unlimited_quota, model_limits_enabled, model_limits`。日志仅取 ID、用户/Key/渠道、模型、quota、usage、类型及时间，不复制 `content`、`other`。
- Key 查询只调用所属用户认证的 `GET /api/token/:id`，不调用可返回完整 Key 的 `POST /api/token/:id/key`。
- Mock 事件只投影类别、计数、状态、模型和 usage；文件最大 10 MiB。读取前由操作者确认事件文件来自预期本地 Mock。健康事件从既有 NDJSON 分类，不主动探测 `/healthz`。
- 费用仅对显式指定的计价模型、精确规则 `tier("base", p * 1 + c * 2)`、且换算率与 `/api/status.quota_per_unit` 完全一致、正组倍率及明确确认无附加费用时计算；否则为 `UNKNOWN`。换算后按 `common.QuotaRound` 的 half-away-from-zero 规则取整。工具不读取数据库或猜测未提供的有效价格配置。
- 可提供上次脱敏 JSON 作为 `-BaselineJsonPath` 比较用户、Key 和消费日志变化。输出 JSON 不含认证信息；中文摘要打印至控制台，输出文件 SHA-256 同时打印。输出目录须符合所有者本机 ACL 要求。
- SSE、失败退款、超时、重试及 A/B 权限隔离只保留 `NOT_RUN` 结构，本版本不触发这些场景。

## 离线测试

```powershell
pwsh -NoProfile -File .\tools\g1\tests\Invoke-G1Acceptance.Tests.ps1
```

## 只读采集示例

在同一个 PowerShell 7 进程中安全提示 Bearer，不要把凭据放进命令行、脚本文件、环境变量或普通文本：

```powershell
$admin = Read-Host '管理员 Bearer' -AsSecureString
$userA = Read-Host '用户 A Bearer' -AsSecureString
$userB = Read-Host '用户 B Bearer' -AsSecureString
$userBearers = @{ '2' = $userA; '3' = $userB }
$tokenIds = @{ '2' = @(1); '3' = @(2) }

.\tools\g1\Invoke-G1Acceptance.ps1 `
  -BaseUri 'http://127.0.0.1:3137' -Port 3137 `
  -ExpectedRevision '<40-char-source-revision>' `
  -ExpectedExeSha256 '<verified-exe-sha256>' `
  -MockEventsPath '<approved-mock-events.ndjson>' `
  -UserIds 2,3 -TokenIdsByUser $tokenIds `
  -AdminBearer $admin -UserBearerById $userBearers `
  -OutputPath '<private-evidence-directory>\g1-readonly.json'
```

首次采集形成脱敏基线；后续添加 `-BaselineJsonPath` 比较。只有从受控管理页面独立核实计价配置后，才传入 `-PricingModel`、`-PricingExpression`、`-QuotaPerUsd`、`-GroupRatio` 和 `-NoAdditionalChargesVerified`。换算率必须与 `/api/status.quota_per_unit` 相同；未验证时不传，结果保持 `UNKNOWN`。工具/API 的只读访问仍可能产生正常管理访问审计日志；监听查询需要系统允许读取本机进程信息。任何动态写入、登录、重启、模型调用均需单独授权。
