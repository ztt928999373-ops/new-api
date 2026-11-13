# D1 自有镜像隔离测试

本配置仅用于全新、无渠道、无支付配置的合成测试。复用仓库根 Dockerfile；原 docker-compose.yml、官方镜像发布、技术主管、运营主管及 U1 文件不变。不是 VPS 生产模板。

## 构建与启动

在干净的独立 checkout 中，核对 `git rev-parse HEAD` 与人工批准的 product/v1 提交；本 Draft 验收先使用本 PR 精确 head。不要直接使用上游 main、calciumion/new-api 或 latest。需要 Docker Linux 引擎、Compose v2 与 PowerShell。

在 PowerShell 当前会话生成两个独立测试随机值，不打印、不提交：

```powershell
$env:D1_SESSION_SECRET = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
$env:D1_CRYPTO_SECRET = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
$env:SUANJIE_SHA = (git rev-parse HEAD).Trim()
./deploy/d1/Build-Images.ps1
docker compose -f deploy/d1/compose.yml config --quiet
docker compose -f deploy/d1/compose.yml up -d --no-build --wait --wait-timeout 180
```

脚本仅构建本地 `suanjie-api:<完整SHA>`，再派生 `suanjie-api-test:<同一SHA>`；无镜像推送或 registry 登录。基础镜像已有 AGPL、NOTICE、第三方清单，测试派生镜像继承。派生层只增加 SQLite CLI 和隔离启动检查，不改应用二进制。不要把测试层用于正式服务。

访问 `http://127.0.0.1:5781/api/status` 验证基本健康；该接口是公共健康接口，不等同管理员登录验收。若需要管理员，使用本地初始化页面设置合成账户及独立随机密码；没有预置管理员密码。注册由数据库选项在首次启动前关闭，支付合规开关保持 false，无渠道且网络 internal，不允许供应商出站。本轮不确认支付合规，不填任何真实凭据。

SQLite 无数据库服务器密码；Session/加密秘密从进程环境注入，未提供可用默认值。环境变量可被本机 Docker 管理员 inspect，禁止将 config 完整展开、inspect 原文或 shell 环境复制到公开日志。两个随机值须在容器重建之间安全保存并复用，丢失或更换可能使会话或加密配置失效。可使用私有目录中由操作者维护的 env 文件，但不得放进构建上下文或 Git。

空卷启动会创建测试标记及三个关闭选项；遇到既有外来数据、缺失标记、重新开启的注册/支付开关、非零渠道均拒绝启动，不自动改写它们。不得挂载 Windows 原实例、DPAPI 文件、生产 SQLite 或已有凭据仓储。容器内不使用 Windows ops serve（其强制回环地址不能用于此端口映射）；此配置不声称等同 Windows DPAPI 备份恢复方案。

## 持久化、备份与回滚

卷为 `suanjie-d1_d1-data`，持久保存完整 `/data`。通常停止使用 `docker compose ... stop` 或 `down`，不要添加 `--volumes`。CI 是一次性数据，才在结尾删除自己的卷。

验收命令见 `.github/workflows/v1-d1-container.yml`：健康、三个许可证文件哈希、重启和强制重建后标记保持、SQLite quick_check、注册拒绝、支付关闭及零渠道。工作流不会推送镜像、不会登录 registry。

备份前停止 app 并确认已退出；禁止直接复制运行中的 SQLite。用相同 SHA 的测试镜像以 `--network none --entrypoint tar`，只读挂载完整停机卷，将整个目录归档到另外的私有备份位置。备份包含数据库及生成的加密材料，应限制目录权限并加密保管；Session/CRYPTO 环境随机值须单独安全备份。记录镜像 ID/digest、源码 SHA、备份校验和及时间。

恢复先建立全新命名卷，停机状态解包完整备份并核对校验和、私有权限及 SQLite quick_check；先以备份对应的旧 SHA 在另一个 Compose 项目/端口验证，再切换。不要覆盖原卷，不自动释放费用责任。任何新版本的 schema 迁移不能仅靠切回旧镜像逆转；必须回滚对应的完整备份，先做独立恢复演练。不要自动 `down -v` 或运行清理全机镜像的命令。

## 后续 VPS 清单与 GHCR 最小方案（本批不执行）

- 先批准 VPS/域名/备份位置及费用；准备 Linux Docker、容量监控、磁盘权限、防火墙与 HTTPS 反代；数据库不公开端口。
- 正式配置需单独审查非 root 运行、资源限制、HTTPS Secure Cookie、精确可信 Origin/代理、网络出站白名单和密钥注入方案；不能直接放开本测试 internal 网络或复制测试数据库。
- 验证真实数据库迁移/恢复和版本兼容后才准备上线；注册/支付/供应商接入各自独立验收，不因容器健康而启用。
- 未来可增加仅人工触发、只允许 product/v1 的发布任务，审核精确 SHA，最小 packages:write，发布到 `ghcr.io/ztt928999373-ops/suanjie-api:sha-<完整SHA>`，固定 digest 部署；不发布 latest、不使用 calciumion 或 QuantumNous 镜像仓库。此批没有 GHCR 登录、发布权限或推送步骤。
- 对外提供对应源码 SHA/源码获取方式，保留 AGPL、NOTICE、原作者归属及 UI 标识。THIRD-PARTY-LICENSES.md 当前声明只覆盖直接依赖，完整传递依赖许可证/SBOM 与 Debian、SQLite 分发材料仍需正式发行前复核，不能凭这三个文件宣称完整法律合规。

## 当前验证边界

开发机 Docker 29.9.2 客户端存在，但 desktop-linux 引擎管道不可用。没有启动 Docker Desktop，以免影响已有容器。因此本机未完成构建、容器健康、重启或数据恢复；Compose 解析/工作流语法成功不等同实际运行成功。新增 CI 仅运行与 D1 构建相关的容器验收，具体结果以对应提交的实际运行日志为准。
