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
