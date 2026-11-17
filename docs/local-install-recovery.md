# 算介 API：单机 SQLite 安装、升级与恢复

范围：批次 C 的显式 `ops` 运维入口，单机、单实例、SQLite、本地磁盘。复用 New API 主程序、安装向导、业务表迁移、管理员后台与 PR #5 凭据加密，不引入第二套后台或密钥仓储。不支持用此工具处理 MySQL/PostgreSQL、多节点、共享盘或外置日志库。

## 安装前

使用已审查的 `product/v1` 完整 SHA，在干净源码副本中安装 go.mod 指定的 Go 与 web/package.json 所需 Bun。开发机已有工具不等于干净 Windows 已完成依赖安装。不要复制原有 `.env`、数据库或密钥进入测试环境。

前端：在 `web` 执行 `bun install --frozen-lockfile`、`bun run typecheck`、`bun run build`。构建前确认干净副本没有 `.env`。随后在仓库根目录构建，必须将源码 SHA 写入版本：

```powershell
$sourceSha = git rev-parse HEAD
go build -ldflags "-X github.com/QuantumNous/new-api/common.Version=$sourceSha" -o new-api.exe .
```

Linux：同样先构建前端，再执行：

```sh
go build -ldflags "-X github.com/QuantumNous/new-api/common.Version=$(git rev-parse HEAD)" -o new-api .
```

每个新二进制 SHA 使用离线恢复到新目录的方式升级。`ops serve` 拒绝直接启动不同版本的旧状态；不在活动库上自动执行结构迁移。默认非 ops 启动机制仍保持上游行为，不具备本手册的进程互斥保证。

## Windows 初始化和启动

先选择当前用户拥有、无不可信可写祖先的本地父目录，例如新建用户资料目录下的专用目录。不要使用共享盘、映射盘、符号链接或普通共享临时目录。工具不会修复已有 ACL，也不会覆盖已有目录或密钥。

```powershell
# 以下路径均是示例；每个输出目录必须尚不存在。
.\new-api.exe ops private-dir --state C:\Users\YOUR_USER\suanjie-local
.\new-api.exe ops init --state C:\Users\YOUR_USER\suanjie-local\state --environment test
.\new-api.exe ops check --state C:\Users\YOUR_USER\suanjie-local\state
$env:SESSION_SECRET = 'synthetic-test-session-secret-only'
.\new-api.exe ops serve --state C:\Users\YOUR_USER\suanjie-local\state --port 3000
```

`test` 示例仅用于合成验收。实际环境应显式选择 development 或 production，并从独立安全配置提供 SESSION_SECRET/CRYPTO_SECRET，禁止沿用示例值。ops 不备份这些环境秘密；如需保持它们保护的数据可用，须另外备份、管理和恢复。供应商 ring 与这些秘密并不相同。

浏览器访问 `http://127.0.0.1:3000`，使用现有安装向导建立管理员，然后进入渠道管理页。新建安全 OpenAI 试点渠道保持禁用。本机 HTTP 测试不代表公网 HTTPS、反向代理、Cookie 或生产部署已验收。

ops serve 不读取 `.env`，明确选择该目录下的 database.sqlite、supplier.ring 和环境；关闭 Redis、批量更新、pprof，并仅监听 127.0.0.1。它保留现有后台与调用逻辑。程序运行期间不要通过其他启动方式访问同一目录。

## Linux

用户应拥有受限本地父目录，目录 0700、文件 0600；ring 的权限不合格时拒绝运行。使用相同命令，将 `new-api.exe` 换成 `./new-api`，路径使用绝对 Linux 路径。

现有 Dockerfile 可构建主程序，默认 docker-compose.yml 面向上游镜像、PostgreSQL/Redis，未配置本分支 ring，含示例密码、latest 镜像和对外端口。它不是本批可直接使用的安全部署方案。不得直接照该 Compose 启动算介 API。本批 Linux 验收使用托管 Ubuntu 单 job；未运行 Docker，不宣称容器部署通过。

## 冷备份：数据库与 ring 分开

1. 停止 ops serve，确认退出。运维命令与服务使用同一 OS 文件锁；服务运行时备份被拒绝。异常退出后 OS 自动释放进程锁，不需要删除 operation.lock。
2. 选择两个不同的私有目录存储数据库与 ring，按日期使用从未存在的新文件名。ring 备份不得提交仓库、镜像或日志。数据库也包含用户资料、哈希、消费记录及可能的上游遗留秘密，须视为敏感资产；文件 ACL 不等于备份内容已整体加密。
3. 运行：

```powershell
.\new-api.exe ops private-dir --state C:\Users\YOUR_USER\suanjie-local\db-backups
.\new-api.exe ops private-dir --state C:\Users\YOUR_USER\suanjie-local\key-backups
.\new-api.exe ops backup --state C:\Users\YOUR_USER\suanjie-local\state --database-backup C:\Users\YOUR_USER\suanjie-local\db-backups\snapshot-01.sqlite --key-backup C:\Users\YOUR_USER\suanjie-local\key-backups\snapshot-01.ring
```

数据库使用 SQLite VACUUM INTO 一致性快照，包含已提交 WAL 数据；不允许直接复制活动数据库主文件。ring 使用原 Provider 的写锁读取。数据库旁的 `.manifest.json` 最后生成，记录环境、配对校验和及加密恢复探针。命令中断时可能留下私有的未完成文件；没有完整 manifest 的备份不得使用。工具不自动删除或覆盖这些文件。

文件 SHA-256 用于配对和检测损坏，不是对恶意管理员的签名认证；必须保护 manifest 和备份目录。备份文件不自动上传或加密归档，异地保管、访问授权和保留策略由运维另行落实。

## 完整恢复与升级

```powershell
.\new-api.exe ops restore --state C:\Users\YOUR_USER\suanjie-local\restored-01 --database-backup C:\Users\YOUR_USER\suanjie-local\db-backups\snapshot-01.sqlite --key-backup C:\Users\YOUR_USER\suanjie-local\key-backups\snapshot-01.ring
.\new-api.exe ops check --state C:\Users\YOUR_USER\suanjie-local\restored-01
.\new-api.exe ops serve --state C:\Users\YOUR_USER\suanjie-local\restored-01 --port 3000
```

- 目标必须是全新目录。原目录及备份不被替换。
- 先验证配对校验和、SQLite 完整性、加密探针、所有凭据版本及渠道引用；错误 KEK 即使 JSON 格式有效也拒绝。
- 恢复后生成新的活动 KEK，保留旧 KEK 用于解密旧版本。禁止手工复制旧 ring 并直接继续加密，避免计数器回退导致 nonce 重用。
- 在新目录执行现有业务迁移与凭据表初始化；校验成功后最后写 ready.json。初始化/恢复中断的目录没有 ready.json，启动、检查和备份均拒绝。失败目录保留供诊断，确认服务未使用后人工清理；重试选新的目录。
- 新版本升级也走这条路径：旧程序停止并备份，用新二进制 restore 到新目录、check、登录和 Mock 验收后再切换。失败时原目录仍完整，使用原二进制返回原目录。
- 备份中的撤销、禁用和版本保持；恢复较旧备份无法自动获知备份后的撤销。禁止未经核对直接恢复对外服务，必须补录最新撤销清单、重新审核渠道启用状态，并对丢失的扣费/订单作业务对账。
- 克隆的恢复目录与原目录不得同时接收业务。供应商 nonce 已隔离，但余额、订单与权限不是多主系统。
- 原始上游旧数据库的任意版本导入、未知 schema、跨数据库迁移不在本工具支持范围；不能将本批加性升级演练扩展解释为任意历史版本升级通过。

## 故障处理与恢复后的验收

缺失 ring、错误密钥、ACL/Unix mode 异常、损坏 DB、缺失 manifest/ready、进程锁竞争均返回固定错误，不打印密钥或底层 SQL。已有 ring 永不自动修复/覆盖。

若进程恰在 Provider 写锁期间崩溃，可能遗留 supplier.ring.lock；它与自动释放的 operation.lock 不同。工具不会盲目删除 Provider 锁。必须证明所有原/旧进程均停止、持有排他的运维控制后，由运维依既有 Provider 手册处理或从备份恢复到新目录。不得通过关闭锁校验继续服务。

恢复后检查：管理员登录及渠道列表、元数据版本、撤销拒绝、新凭据写入、Mock 认证/普通及流式调用、Usage 与扣费退款、缓存重建后禁用渠道仍不可路由。连接测试不代表真实供应商模型验收。

本批测试以合成数据实施。Windows、Linux、SQLite、MySQL、PostgreSQL、race 的实际状态以本批报告为准，不沿用历史 PR 结论。

安全参考：[OWASP Secrets Management](https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html)、[Key Management](https://cheatsheetseries.owasp.org/cheatsheets/Key_Management_Cheat_Sheet.html)。本批不宣称完整 ASVS 或生产部署合规验收。

## 对应源码与构建身份

`VERSION`、`common.Version`、前端 `rv.*` 支持标记和 OCI 标签是发行/运维标签，
不能单独作为实际源码证明。公开页脚和 About 的“对应源码”入口只在下列条件同时满足时显示：

1. Bun/Rsbuild 在构建时确认仓库根目录、`origin` 为本项目
   `https://github.com/ztt928999373-ops/new-api`（或等价 SSH 地址）、工作区干净，记录真实完整 Git SHA；
2. Go 编译器的原生 VCS 信息为 Git、完整 SHA、`vcs.modified=false`；
3. 页脚挂载时刷新状态，刷新中/持久化占位数据不得确认来源；浏览器自身构建 SHA 与正在提供 `/api/status` 的后端 SHA 相同。

入口固定到该 SHA 的 `tree/<SHA>`，不是 `product/v1` 最新分支；所有人可免费浏览/下载
该提交的源码，包含根模块、relaykit、tokenkit、前端、插件、锁文件、Dockerfile、安装/恢复文档、
LICENSE、NOTICE 和 THIRD-PARTY-LICENSES.md。使用原项目链接不能替代修改版本对应源码入口。

从公开仓库克隆后检出要运行的完整 SHA，在**干净工作区**先构建前端，再构建后端：

```sh
git clone https://github.com/ztt928999373-ops/new-api.git
cd new-api
git checkout --detach <完整的已审核SHA>
git status --porcelain
cd web
bun install --frozen-lockfile
bun run build
cd ..
GOWORK=off go build -buildvcs=true \
  -ldflags "-X github.com/QuantumNous/new-api/common.Version=$(git rev-parse HEAD)" \
  -o new-api .
go version -m new-api
```

上述真实SHA版本标签同时保留既有ops升级/状态区分；它本身仍不是源码身份的独立证明。
构建前确认 `git status --porcelain` 为空。构建产物应写入已有忽略路径或源码目录以外，
不要把额外未跟踪源码、配置或旧 `web/dist` 当作当前源码证据。不要使用 `-buildvcs=false`。
忽略文件本身不由 Git 身份证明覆盖；发行方仍需清理并审计 `.env`、本地配置、构建工具、
依赖和产物，完成供应链/构建见证。该 SHA 一致性机制不是签名或独立可复现构建认证。

运行时可核对 `/api/status` 的 `source_revision`、`go version -m` 的 VCS 记录和页面
`<html data-source-revision>` / `window.__APP_BUILD__.sourceRevision`。任一缺失、工作区脏、
上游仓库构建、不同前后端产物或旧缓存混用，页面均显示 `UNKNOWN`，不生成假装对应的链接。
`VERSION` 或 `SOURCE_REVISION` 环境变量不会提供源码身份。

现有 Dockerfile 的上下文排除了 `.git`，Bun 阶段又只复制了 web 目录，因此默认容器构建
会显示 `UNKNOWN`；不能把 OCI revision 标签直接升级为页面已验证 SHA。
本机制不复制 `.git`、GitHub token 或凭据进入构建缓存，不改变发布审批/推送机制。
容器的可验证来源注入/构建见证仍是明确的发行前缺口，未经后续验证不能宣称容器版本追溯已完成。

容器已将 LICENSE、NOTICE、THIRD-PARTY-LICENSES.md 放在 `/licenses/`；二进制、前端包、
桌面包仍应分别核查随附材料和第三方声明。源码树中存在这些文件不等于每种发行包均已履约。
不要把 SHA 一致性、网络源码入口或 NOTICE 检查当作全部 AGPL、供应商、支付或运营合规审查。

## TOTP 种子加密与离线升级（算介 API 修改）

本节用于 P1-G1-TOTP-ATREST。首批密钥模式为**单机、单实例、本地持久目录**；不支持复制可写 ring、多主机、共享盘。`TOTP_DEPLOYMENT_MODE=single-instance-local` 是必需配置，已配置的同一密钥目录有 OS 进程锁；从节点拒绝此模式。不得把分别复制的目录当作多机共享密钥。本批运维命令只接受本地 SQLite 文件；MySQL/PostgreSQL 的 TEXT/schema 和逐行迁移算法有专门回归，但其生产数据库备份/恢复与部署转换仍须另行审核，不能把这些算法测试当作数据库运维工具已经支持三库。

### 存储、启动与注册

- `two_fas.secret` 扩展为 TEXT；`secret_format=1` 与 `totp:v1:` 标记版本化认证加密封装。复用 `pkg/credentialcrypto`：每个 seed 使用随机 DEK、随机 payload nonce，AAD绑定环境、密钥命名空间、用户ID、TwoFA记录ID、用途及逻辑版本；包装层保存KEK版本。
- 新记录在同一事务内先取得记录ID，INSERT参数没有seed；加密写入完成才允许事务提交。激活保持密文。读取状态不解密；激活、登录challenge及step-up保留原有服务端检查后才按需解密。没有正常认证的明文回退。
- TOTP密钥独立于供应商凭据、SessionSecret及用户密码。缺失、损坏、版本错误或绑定不符均失败关闭；已有MFA状态不被改成未启用。错误密钥不计为普通错误验证码，也不自动回退密码。用户显式提交备用码仍按既有单次恢复策略处理。
- 首次注册仅在有效step-up后交付seed/二维码，响应no-store；激活后不再返回seed。生产必须使用直连TLS，或明确设置`TRUSTED_PROXIES`、由可信连接端点声明HTTPS的TLS终止代理。代理必须清理客户端伪造头。test/development密钥只能用于loopback绑定的合成实例，不是公网HTTP生产例外。
- 私钥目录必须私有、持久、独立备份，不进入镜像或数据库。初始化不覆盖既有目录；丢失密钥不会自动生成替代品。

`ops init` 为新的隔离状态创建独立 `totp-keys`，验证密钥快照恢复及probe后发布密钥身份；空数据库通过就绪校验。`ops serve`自动提供同一TOTP配置。普通单机启动必须显式提供（值取自受控初始化的密钥身份，不是示例密钥）：

```sh
TOTP_KEY_DIRECTORY=/private/path/totp-keys
TOTP_ENVIRONMENT=production
TOTP_DEPLOYMENT_MODE=single-instance-local
```

普通启动与localops共用同一个密钥读取、DB READY和逐条解密校验。存在历史TOTP或已有TOTP存储元数据但未配置有效密钥时，启动失败；没有TOTP数据及元数据的部署可以继续非MFA功能，但不能在未初始化密钥时注册TOTP。首次上线必须先完成离线初始化/迁移，不能直接升级后等待用户登录时懒迁移。

### 显式运维入口

所有文件均为隔离/审核操作示意，不授权生产迁移。先停止旧二进制及所有写入；旧版本不认识新锁和READY标志，`--offline`不是自动证明旧进程已停止。不能确认停写就不要执行或切换服务。新版本运行实例占用密钥目录的OS租约，冷操作与之互斥。

```sh
# 普通单机SQLite：key-dir必须是尚不存在的绝对路径。
./new-api totp init-keys --keys /private/totp-keys --environment production
./new-api totp inventory --sqlite /private/source/database.sqlite --keys /private/totp-keys

# 仅对空TwoFA表初始化；已有记录必须走迁移。
./new-api totp initialize --offline --sqlite /private/new/database.sqlite --keys /private/totp-keys

# 历史迁移：原数据库不变，destination不得已存在。
# DB和key备份目录必须分别私有，备份名称不得已存在。
./new-api totp migrate --offline \
  --sqlite /private/source/database.sqlite \
  --destination /private/destination/database.sqlite \
  --keys /private/totp-keys \
  --db-backup /private/db-backups/before.sqlite \
  --key-backup /private/key-backups/before.ring

# 中断后仅恢复有匹配存储元数据的staged目标，使用全新备份文件名。
./new-api totp resume --offline \
  --sqlite /private/destination/database.sqlite --keys /private/totp-keys \
  --db-backup /private/db-backups/resume.sqlite \
  --key-backup /private/key-backups/resume.ring
```

`migrate`先生成一致性数据库备份、独立ring快照和配对SHA256清单，并实际恢复到临时私有目录验证；然后只在全新目标转换。逐行事务与CAS仅改变secret及格式，不改变failed_attempts、locked_until、last_used、created/updated时间、激活状态、auth_version、备用码及pending flow绑定。已知密文只验证，不重复加密；未知格式、异常明文或无法解密记录阻止READY，不输出seed。

逐行迁移允许已完成行保留；故障行事务回滚，READY仍false。重新执行resume继续未完成行，再次验证配对备份恢复；所有适用记录通过后才发布READY。审计输出仅计数/类别，不能把seed或明文SQL放入日志。关闭迁移期间认证；不要手动将READY改true。

`migrate`还生成 `.encrypted` 数据库/密钥备份及相应身份与manifest文件，用新目录实际恢复、轮换活动KEK并验证全部密文后才令目标READY。备份的数据库旁清单及key旁 `.identity.json` 是配套材料，必须一同保护和保留。源库中的历史明文未被就地擦除，须按历史敏感备份政策处理。

旧localops状态使用新的目标目录升级（原目录不变）：

```sh
./new-api ops migrate-totp --state /private/legacy-state \
  --database-backup /private/db-backups/before.sqlite \
  --key-backup /private/key-backups/before.ring \
  --restore-check /private/new-state
```

迁移目标未完成时没有ready.json，禁止启动。选择全新目标重做不改变原输入；底层逐行断点恢复另见totp resume。不能把不完整目录当作已恢复状态，不能人工补ready.json。

### 备份、恢复与KEK轮换

```sh
./new-api totp backup --offline --sqlite /private/live/database.sqlite \
  --keys /private/totp-keys --db-backup /private/db-backups/encrypted.sqlite \
  --key-backup /private/key-backups/encrypted.ring
./new-api totp restore --offline --destination /private/restored/database.sqlite \
  --keys /private/restored-keys --db-backup /private/db-backups/encrypted.sqlite \
  --key-backup /private/key-backups/encrypted.ring
./new-api totp rotate --offline --sqlite /private/live/database.sqlite --keys /private/totp-keys
```

restore目标和密钥目录必须新建，校验配对hash及身份probe；恢复密钥在任何新加密前先rotate。保留旧KEK解密旧记录/备份，rewrap不改变payload和身份，不提供自动删除旧KEK能力。中断的rewrap可重做，旧版本密钥保留，任何丢失/损坏都不能靠生成新密钥绕过。

localops的backup/restore/check同步处理TOTP：原supplier key备份之外产生 `.totp`、`.totp.identity.json`，manifest绑定二者hash；恢复后校验TOTP元数据和全部记录，轮换独立TOTP KEK。备份时必须保留所有配套文件，不能只复制database.sqlite或supplier.ring。

### 回滚与历史备份风险

旧程序不能直接覆盖运行已加密数据库。禁止用SQL批量解密回明文作为普通回滚；默认保持停机并前向修复，或退到同样理解此封装和就绪规则的已审核版本。首次切换尚无这种旧稳定版本时，更不能把原文档的“原二进制返回原目录”当作安全认证回滚承诺。

历史明文源库/备份仍含旧seed：独立加密、限权、保留审计，按批准政策清理所有副本；疑似泄露时需用户受控重新绑定。加密现库和rewrap不能消除过去泄露。恢复旧快照还可能丢失备份后的因素撤销、会话失效或重绑定；必须核对这些变化再决定恢复服务，不自动恢复对外认证。
