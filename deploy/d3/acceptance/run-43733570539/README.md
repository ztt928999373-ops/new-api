# D3 Full：Run 43733570539 独立核验与结项归档

核验日期：2026-11-16（Asia/Shanghai）。结论：**D3 合成隔离动态验收 PASS**；本归档的最终审核、合并和阶段签署仍由项目所有者决定。未验收生产部署、公开注册、支付、真实供应商或商业试点，不启动 C2/V1.5/V2。

## 原始来源与校验

| 项目 | 核验结果 |
| --- | --- |
| Run | [43733570539](https://github.com/ztt928999373-ops/new-api/actions/runs/43733570539)，Full，Success |
| Job | [132871138964](https://github.com/ztt928999373-ops/new-api/actions/runs/43733570539/job/132871138964)，acceptance，Success，页面 10 个步骤均 success |
| Artifact | `d3-43733570539-1`；所有者提供 Artifact ID `14169700096` |
| 原 ZIP | 所有者下载并上传本会话，3,042 字节；ZIP CRC 检查通过，仅含 report.json 和 SHA256SUMS |
| ZIP SHA-256 | `329c501ee9c3c2d58e551c2382eac6455189e2a6b92256e76b8ccb73f4550900`，与 GitHub Run 页面公布的 Artifact digest 完全一致 |
| 原 report.json | 11,242 字节；严格 JSON 解析，无重复键或 NaN/Infinity |
| 原 report.json SHA-256 | `36f09c16d1fcc29bbe701d423fa68876a72c7a942376da0f9d2d9ca50e3bee70`，与 ZIP 内 SHA256SUMS 一致 |
| 验收源码 SHA | `7dc9da0372b7dbd3ebc6a273346523219680ff8e`，与 Run、报告 source_sha、backup.preparation_sha 一致 |
| Compose SHA-256 | `81dc25ccf117b4febfd7620ff03b11f4a44c673380a532c6dcfa9398cbbb67bc`，与验收 SHA 对应的 Git blob 字节独立计算一致 |

GitHub API 当前返回 403，网页直接下载端点不可用；因此明确采用“所有者上传原件 + GitHub 公布 ZIP digest 独立匹配”的核验路径，没有声称 API 直接下载成功，也没有重跑 D3。哈希用于完整性和对应关系核对，不是独立签名。

原 report.json 及 SHA256SUMS 逐字节检查，未修改。完整阅读原日志并检查常见凭据、Bearer、私钥、含凭据 URL 和秘密赋值，未发现未脱敏的实际密钥；原日志包含已有脱敏标记、临时项目标识和内部路径。公开仓库采用最小暴露原则：**不发布原 ZIP、原报告或任何原日志**，只发布白名单摘要和原件哈希，不把删日志后的摘要冒充原报告。

原件由所有者保存，GitHub Artifact 配置留存 7 天，实际过期时间以平台为准；所有者应将原 ZIP 在受控位置安全保管，不能依赖聊天附件或临时 Runner 长期留存。获得原件后可在私有目录解包执行 `sha256sum -c SHA256SUMS`。本目录的 SHA256SUMS 只校验公开的 verified-summary.json，和包内原校验文件含义不同。

## 13 阶段与判定

| 阶段 | 原始报告结果 |
| --- | --- |
| source | success |
| environment | success |
| image | success |
| resource_limits | success |
| seed | success |
| startup | success |
| backup | success |
| restore | success |
| restored_startup | success |
| persistence | success |
| rollback | success |
| diagnostics | success |
| cleanup | success |

`mode=full`、`limits_verified=true`、`outcome=d3_pass`、`workflow_status_before_upload=success`。两组应用日志均 `truncated=false`、`error_code=null`；未见失败、取消、未知或未确认清理字段。独立核对 Job 及所有步骤，包括兜底清理、证据导出、Artifact 上传均成功；报告的 cleanup 也为 success。清理证据来自该临时 Runner 的执行器与 Job，不声称事后登录已回收 Runner 再次列举资源。

环境记录 Docker 28.0.4、Linux x86_64、cgroups v2/systemd、overlay2，MemoryLimit/CpuCfsQuota/PidsLimit 均 true；Docker 与工作目录当时均剩余 91,487,346,688 字节。具体应用/探针限额及卷内权限检查由当前执行器的 resource_limits、seed、restore 阶段实施；公开摘要不含逐项 syscall 跟踪或原始 cgroup 文件，不将这些未保存的原始细节描述为额外证据。

## 固定镜像 Revision 与验收源码不是同一个身份

- 镜像固定引用：`ghcr.io/ztt928999373-ops/suanjie-api@sha256:725d45a3fe8deb3155950a0935bc9873bf0439ef99a425be2dd5f13cbaa21ddf`。
- 镜像 Config Digest：`sha256:a0cc42d43e45ee9ccf69bc12fad8e7c150df78867817dfc727e155e7b186f66a`。
- 镜像 `image_revision=cd8937c602640fc31c0144994a73266c3d792782`：D2 已发布镜像内二进制对应的原始构建源码，不能改写成验收 SHA。
- `source_sha=7dc9da0372b7dbd3ebc6a273346523219680ff8e`：这次 D3 工作流、执行脚本和配置的受信任 Git 检出版本；不是重新构建镜像的证明。

两者不同是预期：D3 使用原有不可变镜像，没有重建或重新发布。本次 `git diff --name-only cd8937c602640fc31c0144994a73266c3d792782..7dc9da0372b7dbd3ebc6a273346523219680ff8e` 显示差异仅在 D2/D3 验收/Registry 脚本、工作流和文档；没有业务源文件、依赖声明、Dockerfile 或前端产品代码差异。该检查限定这两个提交，不把未来主线修改自动认定已由旧镜像验收。

## 证据边界与下一步

本次证明固定镜像在 GitHub 临时 Runner 上的合成 SQLite 冷备份、全新卷恢复、健康/持久化、配对回滚及限定资源清理，不是长期运行、异地灾备、跨版本 schema 回滚、真实凭据恢复、HTTPS/Origin、真实管理员闭环、支付或真实供应商账单验收。

原件已独立核验，公开摘要见 [verified-summary.json](verified-summary.json)。在本目录执行 `sha256sum -c SHA256SUMS` 可验证公开摘要完整性。下一步仅进行 V1-G1 当前主线只读功能审计；任何业务修复另行提出最小 Draft PR，所有者决定合并。
