# 算介 API：Ubuntu 24.04 单节点私有部署（DEPLOY-02）

**先跑合成验收，再决定是否启动私有实例。** 当前只交付资产，不代表真实镜像构建、容器启动或恢复已通过。应用源码固定为已合并的 `product/v1` 提交 `6132c5d8cb5d95e28a6444af4def7da780c2776f`。本分支只包含部署资产；不使用根目录 Compose、官方应用 `latest`、旧 D3 SQLite 镜像或资金候选版本。

## 1. 私有隧道测试与公网生产门禁

本节区分**受控 SSH 隧道私有测试**和**公网生产运营**；测试例外不等于防火墙验收通过，也不降低生产门禁。

受控私有测试仅可用于本次经所有者批准的阿里云日本东京轻量应用服务器实例，并须同时满足：云侧 IPv4 仅放行 TCP 22 和 ICMP，80、443 及业务端口关闭；SSH 使用已核验主机指纹和密钥，deploy 仅按已验证策略转发至 `127.0.0.1:3000`；应用只发布至宿主 `127.0.0.1:3000`，PostgreSQL 只在内部 Docker 网络且没有宿主端口映射；关闭公开注册、充值、供应商接入及公网访问。该实例的 SSH 来源可因管理端动态 IP 暂不设固定白名单，但 TCP 22 仍暴露于公网，必须限定为本次单实例、单一管理隧道测试例外并记录风险。UFW 当前未启用且未通过规则级验收，明确标记为**未验收**，不得称作 PASS。该例外不得复制到其他主机或生产环境。

公网生产仍要求云侧与宿主入站默认拒绝、管理 SSH 来源受限、IPv4/IPv6 暴露逐项核验，并完成 UFW/宿主防火墙与 Docker 规则的独立验证、HTTPS 和公网管理隔离。不得因本次私有测试例外推定生产安全放行。`DEPLOY-UFW-01` 的规则级恢复/回滚实测及 `DEPLOY-KERNEL-01` 的内核兼容性与 Retbleed 处置仍是独立专项，均未通过或关闭。阿里云轻量应用服务器不支持配置 IPv6 地址；其他实例类型仍须独立核查 IPv6 地址、路由和防火墙。

使用所有者批准的专用 Ubuntu 24.04 amd64 主机。建议小流量从 2 vCPU / 4 GiB / 60 GiB SSD 起步；若同机冷构建，优先 4 vCPU / 8 GiB，另备加密异地备份空间。实际容量以预验和使用量为准。不要在现有 Docker、数据库或网关主机照抄安装。勿启用 Docker direct routing、routed 网络或无防护端口转发；不依赖 UFW 单独兜底 Docker 映射。Docker socket 权限等效宿主高权限，不加入 deploy 的 docker 组，不授予 deploy Docker socket，不开放远程 Docker API。该服务器当前 49 GiB 根盘约有 44 GiB 可用，已满足本阶段安装前的静态空间检查；镜像构建峰值和长期数据库/备份容量仍须在 Linux 上实测。

Docker 安装尚未获批，以下仅为执行计划，不得照此提前安装。获批后先核验现有 Docker/容器包、APT 源及候选依赖；若有冲突先停下审核。仅从 Docker 官方 Ubuntu 24.04 APT 源和签名 keyring 获取包；对照 Docker 官方安装文档核验仓库公钥指纹，使用 `gpg --show-keys --with-fingerprint /etc/apt/keyrings/docker.asc` 留存结果，指纹不符即停止。记录当天仓库中经审核的 `docker-ce`、`docker-ce-cli`、`containerd.io`、`docker-buildx-plugin`、`docker-compose-plugin` 精确版本，再用 apt 的 `package=version` 形式安装，不使用浮动最新版。Docker 官方文档提供查询可用版本并按版本安装的流程；安装后记录包版本和 `docker version` / `docker compose version` 输出。详见 [Docker Ubuntu 官方文档](https://docs.docker.com/engine/install/ubuntu/)。

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl git jq openssl util-linux gnupg
sudo install -d -m 0755 /etc/apt/keyrings
sudo curl --fail --silent --show-error --location https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod 0644 /etc/apt/keyrings/docker.asc
sudo tee /etc/apt/sources.list.d/docker.sources >/dev/null <<'EOF'
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: noble
Components: stable
Architectures: amd64
Signed-By: /etc/apt/keyrings/docker.asc
EOF
sudo apt-get update
apt-cache madison docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
# After owner review, set each value to the exact version selected from the signed repository.
ENGINE_VERSION='<approved-exact-version>'
CLI_VERSION='<approved-exact-version>'
CONTAINERD_VERSION='<approved-exact-version>'
BUILDX_VERSION='<approved-exact-version>'
COMPOSE_VERSION='<approved-exact-version>'
sudo apt-get -s install "docker-ce=$ENGINE_VERSION" "docker-ce-cli=$CLI_VERSION" "containerd.io=$CONTAINERD_VERSION" "docker-buildx-plugin=$BUILDX_VERSION" "docker-compose-plugin=$COMPOSE_VERSION"
# Review the simulation and obtain execution approval before replacing -s with a real install.
# After the separately approved install, verify:
sudo docker version
sudo docker compose version
```

上述 `apt-get -s` 只用于模拟，不安装包；正式安装命令须由所有者另行批准并显式使用已审核的精确版本。Compose 脚本要求本机 Unix socket、Engine ≥28、Compose ≥2.24.4；使用经核验的满足版本下限的精确版本。安装前保存 `iptables-save`、`ip6tables-save`、`nft list ruleset`、相关 sysctl、已安装包及 Docker 目录状态；安装后审核 Docker 创建的规则和转发设置，不关闭 Docker 防火墙管理、不设置 `iptables=false` / `ip6tables=false`，也不启用 direct routing、routed 或 `nat-unprotected` 网络。Docker 发布端口会经 Docker 自己的 NAT/过滤路径处理，可能绕过 UFW 常规规则；UFW 不可作为 Docker 端口绑定错误的补偿控制。仅允许经过确认的 `127.0.0.1:3000` 应用映射和无宿主映射的内部 PostgreSQL 网络；安装后重新核对监听、规则和独立外部探测。Docker 官方文档说明其 Linux 网络使用宿主防火墙规则，且发布容器端口可能绕过 UFW；回环绑定和网络模式须按官方端口发布行为单独验证。

若安装验收失败，停止 Docker 服务并用已审核的精确包版本执行回退；只恢复本次安装确实改变的 sysctl 和规则，不清空整套 nftables/iptables，不运行 `docker system prune`，不删除 Docker 数据目录或卷。保存包缓存/版本来源、安装前后规则快照和服务状态。需在独立可丢弃 Ubuntu 24.04 虚拟机先演练安装、规则差异、回滚及残留检查；未完成演练前，不在目标服务器安装。

不得将 deploy 加入 docker 组或授予 Docker socket；Docker 管理操作由 Root 在经过审核的固定 checkout 中执行，或者另行设计并审批严格限定的特权命令。不要使用远程 Docker context。

## 2. 固定镜像并完成隔离验收

获取所有者审阅后的部署分支 checkout（未经审核不要执行；本轮没有推送）。若分支未发布，用所有者批准的私有介质转送已审阅的 bundle 或补丁；不要把资金候选差异或运行环境秘密一起传输。

```bash
# 分支已由所有者发布时：
git clone --branch codex/deploy-01 https://github.com/ztt928999373-ops/new-api.git ~/suanjie-deploy
cd ~/suanjie-deploy
base=6132c5d8cb5d95e28a6444af4def7da780c2776f
test "$(git rev-parse "$base^{commit}")" = "$base"
git merge-base --is-ancestor "$base" HEAD
git diff --stat "$base" HEAD
```

核验部署分支 HEAD 是所有者批准的部署提交，且相对于基准只有部署资产。仅非秘密的 `deploy/private/init-db.sql` 和 `deploy/private/smoke.mjs` bind-mount 给非 Root 容器用户，应为模式 0644；若 checkout 的 umask 更严格，仅对这两个已核验的源码文件调整可读权限，绝不改 `.env` 或备份权限。若已有 D2 人工审核发布、且得到该**同一源码**的 GHCR 不可变 digest，优先 `bash deploy/private/manage.sh import "$APP_GHCR_DIGEST"`；变量必须是 `ghcr.io/ztt928999373-ops/suanjie-api@sha256:<64位>`。仅有 CI PASS 或 `sha-…` 标签不是已发布证明。私有 GHCR 的读取授权另行配置，勿把访问令牌写入本目录或命令行。没有可核验的同版 digest 时，从 Git 对象冷构建：

```bash
bash deploy/private/manage.sh build
```

构建只将固定 Git 对象归档传给仓库根 Dockerfile，复用冻结 Bun 依赖、固定构建镜像和后端嵌入前端流程。不会从工作目录复制私有文件，不推送镜像。构建/导入均核验 Linux amd64、OCI 源码 revision、来源、入口及镜像内三份许可文件的 SHA-256；记录输出的镜像 ID。已有同标签镜像不会被导入操作覆盖。镜像标签和本机 Docker 管理员同属信任边界，不是签名证明。

选择并审核官方 PostgreSQL 17 的安全补丁版本。示例为 `17.12-bookworm`，服务器执行时先复核公告。下面使用精确 tag 查得顶层 manifest digest，核实为官方仓库且包含 `linux/amd64` 后采用，不使用浮动 `17` 或 `latest`：

```bash
pg_tag=docker.io/library/postgres:17.12-bookworm
docker buildx imagetools inspect "$pg_tag"
read -r -p '粘贴上面已核验的顶层 sha256 digest: ' pg_digest
[[ $pg_digest =~ ^sha256:[a-f0-9]{64}$ ]] || exit 1
PG_IMAGE="$pg_tag@$pg_digest"
install -d -m 0700 "$HOME/.local/state/suanjie"
smoke_state="$HOME/.local/state/suanjie/smoke-$(date +%s)"
bash deploy/private/manage.sh smoke "$smoke_state" "$PG_IMAGE"
```

仅在 `SMOKE=PASS` 且退出码 0 时继续。使用新项目、新卷以及只有内部网络的 App、PG、固定 Bun 合成上游，无主机映射。脚本在启动前检查实际渲染网络和配置，启动后检查容器绑定；覆盖健康、全新 PostgreSQL、一次性 Root 初始化、加密登录及角色、注册拒绝、支付入口标志关闭、模型 Key 创建与一次 mock 响应（含 usage）。无外部供应商请求，不测试真实资金、SSE 或支付事务。脚本仅清理本次唯一测试项目的容器、网络、卷，保留私有合成状态目录；失败不打印响应正文、Key、token 或完整环境。若失败，先用本轮专属项目定位问题，不进入下一步，也不关闭访问保护求通过。

## 3. 私有实例及首次管理员

在同一已完成合成验收的 Linux 环境，明确批准私有实例后由所有者执行：

```bash
state="$HOME/.local/state/suanjie/private-01"
bash deploy/private/manage.sh init "$state" "$PG_IMAGE" 3000
bash deploy/private/manage.sh start "$state"
bash deploy/private/manage.sh status "$state"
```

`init` 只创建不可覆盖的新目录（0700）与 `.env`（0600），生成四份独立的 256-bit 随机值：PG 引导管理员密码、应用数据库角色密码、会话密钥、加密密钥。应用数据库角色拥有本库以执行本库迁移，不是超级用户；PG 数据卷不映射宿主端口，应用使用独立 bridge 连库。Redis 留空、批量更新和内存缓存显式关闭。基准版没有候选中的启动门禁；不要改环境、覆盖 Compose 或复用已有卷。应用口只映射宿主 `127.0.0.1:3000`；常规私有实例有出站路由，初期不设置任何外部渠道或支付配置。隔离 smoke 不接入此出站网络。

从经过主机指纹验证的管理端执行，替换 `SERVER`：

```bash
ssh -N -o StrictHostKeyChecking=yes -o ExitOnForwardFailure=yes \
  -L 127.0.0.1:13000:127.0.0.1:3000 deploy@SERVER
```

在该管理机访问 `http://127.0.0.1:13000`，且仅在出现全新设置页时设置唯一 Root 名和密码，关闭演示模式；不要把首次初始化 URL 暴露给其他人。初始化后确认 Root 能登录、公开注册为关、在线充值无支付方式；不确认支付合规、不填收款配置。注册关闭来自基准默认和数据库选项，并非不可修改的门禁；恢复旧数据后尤其要重查。`/api/status` 成功只说明服务健康，不代表登录、支付或对外可用。

当前仅支持可信、单管理员宿主的隧道模式；`SESSION_COOKIE_SECURE=false` **不是公网配置**，SSH 保护远程链路，宿主回环与容器桥仍是本机信任边界。若后续批准域名和 HTTPS，先在私网/VPN入口验证 TLS、`SESSION_COOKIE_SECURE=true`、准确 `SESSION_COOKIE_TRUSTED_URL`、明确可信代理地址、阻断公网 setup 和公网管理，并验证 SSE 代理行为；本模板不启用公网反代。适用参考：OWASP ASVS 5.0.0 V6/V7/V12、Authentication/Session Management Cheat Sheets；此合成验收不宣称全面 ASVS 合规。容器环境可由 Docker 管理员读取，禁止发布 `docker inspect` 全文、展开后的 Compose 或私有日志；禁止 `set -x`。密钥不会写入镜像或 Git。

本轮不接入真实供应商、开放注册、充值或收费。受管供应商凭据需要另验 Linux 专用私有 keyring 和数据库记录，不能把 Windows DPAPI 材料复制给 Linux，也不能用旧渠道明文方式绕过。当前基准的 TOTP 运维初始化只支持 SQLite；本 PostgreSQL 配置不启用、不声明支持 TOTP，不用 SQL 伪造 READY。若要开放非隧道管理或接入真实供应商，先单独验收相应能力。

## 4. 备份、重启与恢复

```bash
# 在维护窗口停止 app 保证 app 卷和数据库属于同一停止窗口；PG 不停
bash deploy/private/manage.sh stop-app "$state"
bash deploy/private/manage.sh backup "$state"
# 审阅成功输出并保留对应镜像；按需恢复服务
bash deploy/private/manage.sh restart "$state"
# 或对两个容器都停机（卷保留）
# bash deploy/private/manage.sh stop "$state"
```

该单库备份包含 custom `pg_dump`、应用数据/日志目录、配套 `.env`、版本及校验和。引导角色由原镜像初始化脚本重新生成，不是全 PG 集群备份。不并行变更设置或从其他入口写库；不要复制运行中的 PGDATA。只把拥有 `SHA256SUMS` 且校验通过的目录作为备份候选。备份包含会话秘密和潜在用户数据：先加密再离机传输，定期演练恢复；不要把备份和解密密钥保存在同一处。保留事前已验证的原始 GHCR digest，或用 `docker image save` 将实际本地镜像随加密备份另存，以便恢复可验证的镜像 ID；仅重新构建同 SHA 不保证得到原镜像 ID。

```bash
# 原实例仍保留；新项目的私有端口与原实例不同
# TRUSTED_BACKUP 为本方生成、已认证来源、已解密且权限 0700/0600 的完整目录
new_state="$HOME/.local/state/suanjie/recovery-01"
bash deploy/private/manage.sh restore "$new_state" "$TRUSTED_BACKUP" 3001
bash deploy/private/manage.sh start "$new_state"
```

恢复只写入全新卷且不启动 app；`start` 是另一步。先通过独立 SSH 隧道验证恢复的登录、关闭注册/支付状态和预期数据，再由所有者切换，避免两套实例并发服务。脚本不会覆盖原卷、自动删生产卷或降级数据库。失败可能留有部分恢复卷；保留并调查，不重用作干净恢复。回滚使用升级前的镜像、部署配置、经过验证的配套备份恢复到新项目；不得把旧镜像直接指向已迁移卷。校验和仅证明完整性，不证明来源可信，切勿恢复不可信 dump 或 tar。

## 5. 许可证与验收边界

遵循基准 `LICENSE`、`NOTICE`、`THIRD-PARTY-LICENSES.md`；镜像保留三者，UI 保留 New API 贡献者署名、QuantumNous 原项目可见链接；明确标记算介修改。按 AGPL §13 向远程交互用户显著提供与运行镜像对应的免费源码获取方式，含适用的构建及部署材料，不包含运行秘密；许可证合规不能替代供应商转售授权和商业运营审核。执行人还需验证 Linux Docker 冷构建、实际 PostgreSQL 启动及恢复演练，Windows 静态核验不能代替这些结果。
