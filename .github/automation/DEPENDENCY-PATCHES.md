# 独立安全补丁验收

目标 product/v1，业务基线77d5158c。AI-assisted。依赖修复与 #23 自动化逻辑分开审核，不修改费用后台 #22 的分支。

本批目标：Go1.26.11（root/relaykit）、Brotli1.2.7、compress1.20.2、x/net0.60.0、x/text0.42.0（由x/net0.60.0要求）及配套x/crypto0.57.0、x/sys0.48.0、x/image0.45.0。relaykit 原1.25.1的支持边界提升到1.26.11，因为本次新标准库漏洞没有受支持1.25补丁覆盖。tokenkit 依赖保持不变，其独立扫描也继续验证。

验收必须包括：root/relaykit/tokenkit 的 GOWORK=off 独立构建与测试、三个模块漏洞扫描、SQLite/MySQL/PostgreSQL 实际安全和费用回归。保留既有 CI 与安全矩阵，不降低门槛。ClickHouse 单独日志数据库路径尚需专门环境验证；本批不修改 GORM/driver 版本，不声称 ClickHouse 已验证。

本地已校验官方 Go 下载清单的 SHA256 并安装 Go1.26.11。relaykit 独立 go build ./... 与 go test ./... 均退出0。本地 govulncheck 无法访问 vuln.go.dev（网络策略 Forbidden），此结果不是漏洞扫描通过；改用 GitHub Actions 的完整 JSON/文本扫描证据。

VULNERABILITIES.md 列出初始22个不同GO ID及各模块发现/修复版本。初始完整 Symbol Results 在 evidence/，含所有工具给出的例子链。修复后的实际结果以此 PR 对应 SHA 的 Actions artifacts 为准；未运行和跳过不计通过。禁止自动合并、部署、启用注册/真实支付。

本地 tokenkit 的独立 go build ./...、go test ./... 也退出0。根模块新版本的7组下载及校验和已写入 go.sum；compress 官方模块 ZIP 的存储重定向被本机策略拒绝，改由该模块官方 GitHub 源码下载并通过 Go sumdb 验证。根模块完整依赖图/构建/三库验证以 CI 为准，本地未完成，不列为通过。
