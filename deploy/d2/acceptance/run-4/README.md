# D2 Run #4：用户提供的原始 JSON 归档

本目录仅归档用户上传的 `D2-Run4-acceptance-user-provided.json`，不执行验收、发布或部署，不修改产品代码，也不据此自动关闭 D2。

## 来源与完整性

- 来源：本次会话中用户上传的完整 JSON 附件；“Run #4”为用户提供的运行标识。
- 原文件：[D2-Run4-acceptance-user-provided.json](D2-Run4-acceptance-user-provided.json)，12,700 字节，逐字节保留，包括已有脱敏占位符、缩进与换行。
- SHA-256：`7846130596a38bf10fcc5f04d13dfd20f5c2dda8ddbd87a4591ee2b2d9bc2b08`；校验文件：[SHA256SUMS](SHA256SUMS)。
- 上述摘要由收到的附件计算，并与归档副本比较；用户未另行提供可信摘要或签名，不能据此证明上传前的来源真实性。
- JSON 未提供 Actions run ID、URL 或 attempt。本次没有从 Actions 下载原报告，不能独立确认其与特定工作流运行的关联。
- 归档检查日期：2026-11-15。报告中的容器时间保留 UTC 原值；本次没有重跑 D2。

## 固定身份核对

以下字段与检出提交 `28c752c7b5a141745ac9434d91ccd0cce31442ab` 的 `.github/automation/d2_container_acceptance.py` 常量核对一致：

| 字段 | 附件记录 |
| --- | --- |
| image | `ghcr.io/ztt928999373-ops/suanjie-api@sha256:725d45a3fe8deb3155950a0935bc9873bf0439ef99a425be2dd5f13cbaa21ddf` |
| image_revision | `cd8937c602640fc31c0144994a73266c3d792782` |
| local_config_digest | `sha256:a0cc42d43e45ee9ccf69bc12fad8e7c150df78867817dfc727e155e7b186f66a` |
| platform | `linux/amd64` |
| verifier_sha | `28c752c7b5a141745ac9434d91ccd0cce31442ab`，与归档前读取的 `product/v1` 一致 |

本次核对报告字段与代码约定，没有重新拉取或检验远端镜像。

## 13 项验收记录

附件 `phases` 与验证器的 13 个阶段名称及顺序一致，全部记录为 `success`；以下是报告内容核对结果，不是本次新增的动态验证。

| 阶段 | 附件结果 |
| --- | --- |
| capacity | success |
| pull | success |
| identity | success |
| seed | success |
| licenses | success |
| startup | success |
| health | success |
| network | success |
| policy | success |
| stopped_database | success |
| restart_persistence | success |
| final_database | success |
| cleanup | success |

附件没有顶层 `error_code` 或 `cleanup_error`。`free_before=94939852800`、`free_after_pull=94692970496`、`free_after_tests=94689828864` 字节均大于验证器要求的 5 GiB；`image_size=231903548` 字节。这些是原运行记录，与当前归档工作区的磁盘检查不同。

## 必须保留的诊断边界

- `seed_preflight.status=PASS`，但其辅助证据中的部分身份和存储状态仍为 `UNKNOWN`。
- `diagnostics.status=incomplete`、`diagnostics.errors=[]`、`diagnostics.storage.status=UNKNOWN` 均原样保留。验证器在 storage 状态为 UNKNOWN 时会将 diagnostics 标为 incomplete，不能将“无 errors”解释为诊断完整。
- 辅助测量的 `access_status=PASS` 和各访问检查通过，不等于原进程 SQLite 写打开或 syscall errno 已被测得；`database_write_open`、`sqlite_open_errno` 仍为 `UNKNOWN`。
- 最终采集点是 `final_database`，容器 `Status=exited`、`ExitCode=0`、`OOMKilled=false`，`runtime_kind=completed_execution`。`Health.Status=unhealthy` 也保留；两条历史 health 记录的 ExitCode 均为 0。不能把停机后的快照改写成当前运行健康，也不单凭该值否定报告较早的 health 阶段记录。
- seed 大小为 12,288 字节，最终数据库大小为 929,792 字节，`database_size_matches_seed=false`。大小变化本身不足以证明复制损坏或数据库完整性；本次仅核对 phases 中数据库检查的记录，没有获得或打开 SQLite 文件。

## 敏感信息检查与复核

归档前完整读取 JSON，检查全部键、字符串及内嵌日志，未发现明显的明文密钥、密码、Bearer 凭据、GitHub/API token、私钥或含凭据 URL。附件已含 `[REDACTED SENSITIVE LINE]` 和 `[REDACTED OPAQUE VALUE]`；本次没有恢复、替换或再格式化这些内容。镜像摘要、提交 SHA、临时卷名、进程 ID、UID/GID 与本地路径为报告元数据，原样保留。此检查不构成对上传前脱敏过程的独立认证。

可在本目录执行以下只读检查，无需 Go/Bun、依赖安装、容器或产品测试：

```sh
python3 -m json.tool D2-Run4-acceptance-user-provided.json > /dev/null
sha256sum -c SHA256SUMS
```

本次另以 Python 标准库拒绝重复 JSON 键和 NaN/Infinity，核对固定身份、13 项结果和磁盘门槛，并以 `cmp` 确认归档文件与上传附件完全相同。仅形成文档归档，合并与后续运行均由用户另行决定。
