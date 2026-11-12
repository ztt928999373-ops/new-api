# PR #23 漏洞核实与风险分级

扫描基线 head：5ced090ac0d2661ddbc02fcf2c6a224b20def6fa；扫描 merge：e35ecb0a8574fbe17b2452fbe5883c5ea42841f9；未变更生产配置。

原始运行：https://github.com/ztt928999373-ops/new-api/actions/runs/43224976595 。root 12 / relaykit 15 / tokenkit 0 项符号可达告警，共 22 个不同 GO ID。0 项只代表本次工具/平台/数据库视角，不是无漏洞证明。

逐项与官方 golang/vulndb data/osv/GO-*.json 核对 affected ranges；原始 Symbol Results 和全部调用链保存在 evidence/ 下。OSV 未提供 CVSS，以下 P1/P2 是项目修复优先级，不冒充官方严重度，也未将静态可达等同线上可利用。

| GO ID（官方记录） | 模块与发现版本 | 最低修复版本（当前 Go 分支） | 优先级与影响范围 |
|---|---|---|---|
| [GO-2026-6888](https://pkg.go.dev/vuln/GO-2026-6888) | root: github.com/andybalholm/brotli@v1.2.0 | root: github.com/andybalholm/brotli@v1.2.7 | P1：上游 Brotli 解压内存耗尽，Gemini 模型获取及响应读取。 |
| [GO-2026-6865](https://pkg.go.dev/vuln/GO-2026-6865) | root: github.com/klauspost/compress@v1.18.7 | root: github.com/klauspost/compress@v1.20.2 | P1：Zstd/FSE/Huffman 特制块 panic；上游 body 解码及日志 ClickHouse 压缩链。 |
| [GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859) | root: net/http@go1.26.10; relaykit: net/http@go1.25.1 | root: net/http@go1.26.11; relaykit: net/http@go1.26.11 | P1：HTTP/1 MaxBytesReader 超限且并发写响应头可导致服务崩溃；需 Go1.26.11。 |
| [GO-2026-6629](https://pkg.go.dev/vuln/GO-2026-6629) | root: golang.org/x/text@v0.37.0 | root: golang.org/x/text@v0.41.0 | P1：PRECIS 特制文本 panic，数据库驱动认证/连接链；需三库回归。 |
| [GO-2026-6617](https://pkg.go.dev/vuln/GO-2026-6617) | root: golang.org/x/net@v0.55.0; relaykit: net/http@go1.25.1 | root: golang.org/x/net@v0.60.0; relaykit: net/http@go1.26.9 | P1：HTTP/2 HPACK 并发编码 race/crash；依赖实际 HTTP/2 服务路径。 |
| [GO-2026-6613](https://pkg.go.dev/vuln/GO-2026-6613) | relaykit: net/http@go1.25.1 | relaykit: net/http@go1.26.9 | P1：HTTP/1 CONNECT 2xx 后连接去同步；需实际 CONNECT 使用路径。 |
| [GO-2026-6612](https://pkg.go.dev/vuln/GO-2026-6612) | root: golang.org/x/net@v0.55.0; relaykit: net/http@go1.25.1 | root: golang.org/x/net@v0.60.0; relaykit: net/http@go1.26.9 | P1：HTTP/2 flow-control 重复归还可突破流控、消耗资源。 |
| [GO-2026-6611](https://pkg.go.dev/vuln/GO-2026-6611) | root: golang.org/x/net@v0.55.0; relaykit: net/http@go1.25.1 | root: golang.org/x/net@v0.60.0; relaykit: net/http@go1.26.9 | P1：反复 initial window 变更造成 CPU 耗尽。 |
| [GO-2026-6603](https://pkg.go.dev/vuln/GO-2026-6603) | root: golang.org/x/net@v0.55.0; relaykit: net/http@go1.25.1 | root: golang.org/x/net@v0.60.0; relaykit: net/http@go1.26.9 | P1：HTTP/2 trailer 头造成内存耗尽。 |
| [GO-2026-6222](https://pkg.go.dev/vuln/GO-2026-6222) | root: golang.org/x/image@v0.41.0 | root: golang.org/x/image@v0.45.0 | P1：用户/上游 WebP VP8L 图像解码内存耗尽。 |
| [GO-2026-5972](https://pkg.go.dev/vuln/GO-2026-5972) | relaykit: encoding/asn1@go1.25.1 | relaykit: encoding/asn1@go1.25.13 | P1：ASN.1 无递归深度上限；证书/DER 解析栈与资源耗尽。 |
| [GO-2026-5970](https://pkg.go.dev/vuln/GO-2026-5970) | root: golang.org/x/text@v0.37.0 | root: golang.org/x/text@v0.39.0 | P1：Unicode normalization 无效输入无限循环；Passkey RPID/IDNA 等链。 |
| [GO-2026-5061](https://pkg.go.dev/vuln/GO-2026-5061) | root: golang.org/x/image@v0.41.0 | root: golang.org/x/image@v0.43.0 | P1：WebP alpha 通道长度不匹配导致 panic。 |
| [GO-2026-5037](https://pkg.go.dev/vuln/GO-2026-5037) | relaykit: crypto/x509@go1.25.1 | relaykit: crypto/x509@go1.25.11 | P2：X.509 候选主机名解析复杂度；TLS/证书处理条件待核实。 |
| [GO-2026-4961](https://pkg.go.dev/vuln/GO-2026-4961) | root: golang.org/x/image@v0.41.0 | root: golang.org/x/image@v0.42.0 | P2：32 位 WebP 大图 panic；amd64 扫描不能证明 32 位实际暴露。 |
| [GO-2026-4602](https://pkg.go.dev/vuln/GO-2026-4602) | relaykit: os@go1.25.1 | relaykit: os@go1.25.8 | P2：os.Root FileInfo 逃逸；relaykit 库未独立提供文件服务，需宿主使用 Root 的路径核实。 |
| [GO-2026-4601](https://pkg.go.dev/vuln/GO-2026-4601) | relaykit: net/url@go1.25.1 | relaykit: net/url@go1.25.8 | P1：IPv6 host 字面量 URL 解析错误，影响敏感 URL 脱敏/解析边界。 |
| [GO-2026-4341](https://pkg.go.dev/vuln/GO-2026-4341) | relaykit: net/url@go1.25.1 | relaykit: net/url@go1.25.6 | P1：URL query 解析内存耗尽，maskURLQuery 链。 |
| [GO-2025-4011](https://pkg.go.dev/vuln/GO-2025-4011) | relaykit: encoding/asn1@go1.25.1 | relaykit: encoding/asn1@go1.25.2 | P1：DER 解析内存耗尽；证书解析链，实际不可信证书入口待核实。 |
| [GO-2025-4010](https://pkg.go.dev/vuln/GO-2025-4010) | relaykit: net/url@go1.25.1 | relaykit: net/url@go1.25.2 | P1：带括号 IPv6 hostname 校验不足，URL 解析/安全策略边界。 |
| [GO-2025-4009](https://pkg.go.dev/vuln/GO-2025-4009) | relaykit: encoding/pem@go1.25.1 | relaykit: encoding/pem@go1.25.2 | P2：非法 PEM 解析二次复杂度；不可信 PEM 是否可到达需核实。 |
| [GO-2025-4007](https://pkg.go.dev/vuln/GO-2025-4007) | relaykit: crypto/x509@go1.25.1 | relaykit: crypto/x509@go1.25.3 | P2：证书名称约束检查二次复杂度，取决于证书处理路径。 |

## 修复边界与剩余风险

- #23 保持自动化修复，不混入五组运行时依赖升级。漏洞扫描继续阻断：不得关闭、continue-on-error 或无依据忽略。
- 独立依赖修复 Draft PR：Go1.26.10→1.26.11；relaykit Go1.25.1 已无当前受支持安全补丁覆盖全部新告警，独立扫描/构建也升级至1.26.11；brotli1.2.7、compress1.20.2、x/net0.60.0、x/image0.45.0。x/text 的独立告警最低修复为0.41.0，但x/net0.60.0的模块约束要求实际选择x/text0.42.0，配套x/crypto0.57.0、x/sys0.48.0。实际候选补丁见独立 Draft PR #24，不声称这些是最新版本。
- compress 影响 ClickHouse 路径，x/text 可经数据库驱动使用；依赖修复必须保留完整 root/relaykit/tokenkit 独立构建测试、三数据库实际回归；ClickHouse 单独配置路径未验证时明确列为限制。
- relaykit 的“标准库可达”例子很多来自 fmt/Stringer/错误处理的保守静态调用图；库本身没有独立 HTTP 服务器。无需删除扫描或猜测无风险，使用受支持的补丁工具链后重扫。
- tokenkit 成功使用 Go1.26 系列工具链；root/relaykit 同一 GO ID 重复计数不代表两个独立漏洞。
- 初始 root 还报告 3 个 imported-package、8 个 required-module 告警，relaykit 31/16；这些不计入上述符号可达数量。当前不可达不是永久豁免，后续 JSON 证据一并保留。

## 完整扫描证据校验

- relaykit-initial-scan.txt: SHA256 `6578b9c262791a110fe03414209e13b2a832ac8f487906c30b04ce99fddd613d`
- root-initial-scan.txt: SHA256 `7f601e1a076a9461aad5d3e55345644c9a371a0c89c43b3ecff58ab1736ed77c`
