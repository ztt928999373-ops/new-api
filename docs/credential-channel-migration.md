# 渠道加密接入：当前范围与迁移手册

## 当前实现

新增 supplier_credentials 与 supplier_credential_versions 两表：身份/owner/状态/当前版本/
revision 和独立的不可变版本密文。仓储提供 Create、Replace、SetStatus、CurrentVersion、
GetForUse，所有替换/状态更新使用 revision CAS；版本唯一冲突必须回滚 head 更新。
新增平台管理员基础接口：POST /api/channel/credentials（新增）、GET /:id（metadata/版本）、
PUT /:id（替换，相当于供应商 Key 轮换）、POST /:id/status（禁用/恢复/撤销）、
POST /:id/rewrap（现有版本的 DEK 重新包装到当前活动 KEK）、
POST /:id/test（固定 OpenAI 官方 /v1/models，禁止重定向，只验证认证连通，不验证模型）。
接口复用 ChannelRead/ChannelSensitiveWrite/ChannelOperate，不能读取其他用户 BYOK 秘密，
不返回完整 Key。当前 KEK 激活由 KeyProvider 运维操作负责，不经普通后台接口；
KEK Rewrap 的数据库批量管理、完备审计管理界面仍需实现。
Resolver 访问审计持久化到 supplier_credential_access_events；凭据创建、替换、状态和 Rewrap
与审计在同一数据库事务中提交。管理接口另复用原管理审计。审计持久化失败不得准入/提交变更。

Channel 增加仅服务端可见的 CredentialRef。SetupContextForSelectedChannel 经现有路由授权后
调用 BindChannelCredentialRequest；引用存在但 Resolver 未配置、状态不可用或认证失败时返回 503，
绝不回退到旧明文。原 Adapter/Relay 接口不变，明文仅在请求级 Channel 副本/上下文使用，
不写回共享缓存。此过程仍有运行时明文，不能宣称平台无法访问凭据。
目前租户为 default；平台身份只用于已授权的渠道调用，不是用户可自行指定的管理身份。
默认关闭新存储：只有显式配置 SUPPLIER_CREDENTIAL_KEYRING_PATH 和
SUPPLIER_CREDENTIAL_ENVIRONMENT（development/test/production）才初始化存储及访问审计。
新表必须在备份后通过受控 schema 安装预先建立；启动检查缺表时拒绝初始化，不自动创建。
部署此版本前仍需备份：原有 New API AutoMigrate 会给 channels 增加 credential_ref 元数据列。
配置必须指向既有独立 ring；不会生成 KEK 或自动迁移旧 Key。未配置时新管理接口拒绝服务，
已有 legacy 渠道仍走原路径；存在加密引用但无法解析时拒绝调用。
迁移后的 Channel.Update 拒绝明文 Key 替换，需经凭据仓储 Replace。普通 Key 查看只能得到空值。
加密多 Key 的名称等普通更新保留原 Key 数量/状态；slot 管理迁移完成前，替换 bundle 不允许改变数量。
直接 SQL 写入和其他供应商写回路径尚未统一，因此迁移器有严格类型/配置门槛。

## 历史迁移的实际边界

MigrateDisabledOpenAI 是显式服务方法，未注册启动自动迁移或 HTTP 迁移端点。
仅接受手动禁用的 OpenAI 渠道，保留完整多 Key 原文，保持 GetKeys/索引状态兼容。
存在 Other、Settings、HeaderOverride、ParamOverride 等额外配置时拒绝；其他供应商/OAuth
一律拒绝。不得绕过此门槛扩展迁移范围：OAuth 直接写回、任务 PrivateData.Key、
余额/探测/复制等旁路未全部改完前，不可以声称完成全渠道接入。

传入备份路径与 SHA256，操作前确认非空文件及摘要匹配；该检查只证明备份文件存在且未变，
不能证明它包含正确数据库或一致时间点，操作者必须额外执行恢复/数据库身份校验。
事务内锁定渠道→建立加密记录→解密比对→清空旧 Key＋保存引用。重复执行验证已有引用，
不会重新创建凭据。事务失败保持旧 Key；已预留的加密 nonce 不回收。
数据库 schema 创建单独执行，MySQL DDL 不应宣称能随数据事务回滚。

## 操作前置条件与恢复

1. 当前仅合成数据可演练。真实迁移尚未批准，禁止直接调用生产数据库迁移。
2. 保存一致性数据库备份：SQLite 备份 API/VACUUM INTO；MySQL/PostgreSQL 用各自可靠备份工具。
   对备份执行恢复校验，记录数据库身份、时间点、应用版本、文件摘要、需要的 KEK 版本。
3. 单独保存 KEK 恢复材料，不能放在数据库备份目录或同一备份权限域。
4. 停止旧版本写入、后台刷新和相关任务；只在已分类且允许的渠道上分批迁移。
5. 验证新引用、解密内容、多 Key 顺序、旧 Key 为空、备份仍存在，配置 Resolver 后才能启用调用。
6. 故障：单条失败事务回滚，可重复执行；已完成条目不重复加密。不中途删除备份或密钥版本。
7. 完整回滚使用隔离恢复环境，不以启动旧二进制直接读取加密库代替恢复。
   恢复旧 ring 后先轮换 KEK 再加密，核对撤销清单，保持外网出口关闭直到 Mock 验证完成。

## 实际测试与未验证项

- Windows：三个真实引擎 SQLite、MySQL 8.4、PostgreSQL 17 的仓储用例，各执行三轮。
  覆盖 owner 隔离、8 个并发替换仅一次成功、版本唯一约束/审计写入失败触发事务回滚、
  禁用/恢复/撤销，以及 KEK 轮换＋数据库 Rewrap 后保留内容密文且旧凭据可解析。
- SQLite：实际 VACUUM INTO 备份恢复，缺少备份拒绝、重复迁移、多 Key/缓存副本和撤销拒绝。
- MySQL/PostgreSQL 的历史 Channel 迁移、备份恢复尚未执行；不能从仓储结果推断通过。
- 单节点准入屏障及在途取消见下方增量记录；跨节点取消与任务版本迁移未完成。
- 通用 BYOK HTTP API、OAuth 刷新/任务/全部秘密字段兼容仍未完成，不开放真实密钥接入。
- 管理 API 使用合成凭据＋Mock RoundTripper 测试，没有真实 OpenAI 调用。
- 与 PR #4 的本地集成分支将其 8 场景改为从加密数据库 Resolver 读取，Windows 已通过；
  不代表 GitHub PR 已合并，也未完成异步任务及全部供应商的集成。
- Docker 测试容器仅绑定 127.0.0.1，使用合成密码；不是公网部署。

## 最小安全修复批次（2026-11-08，待审查、未推送）

本批保留 a38a817→07425fb 已有日志修复和原有受限迁移白名单，不执行真实历史迁移，
不增加数据库结构、不修改 OAuth 刷新与任务快照。仅对受控凭据引用施加以下边界：

### 本机密钥文件权限

Windows 在每次读取、写入和替换时验证 ring、目录及祖先链。只支持本地固定盘/RAM 盘，
拒绝 UNC、网络盘、重解析点、不可信所有者、空 DACL、未审查的 ACE 或不可信写入权限。
ring 必须有受保护 DACL；仅运行身份、SYSTEM、Administrators 可获准访问。祖先可保留普通
读取/遍历/创建子目录权限，但不能让不可信身份删除子项、改写目录、修改 ACL 或取得所有权。
Windows 系统卷根目录的 TrustedInstaller 所有者只在祖先检查中允许。

新 ring、锁和临时文件在创建时就使用受保护 DACL。替换前将目标所有者及 DACL 复制到临时文件，
验证后才写入秘密；替换前后再次核对实际权限。权限不安全一律拒绝，不自动放宽或修复现有目录。
InitializeLocalDirectory 仅显式创建新私有目录；不得把它当作修改现有权限的工具。
本机测试使用新建的受控临时根目录，普通共享 TEMP 可能因额外可写 ACE 被拒绝，这属于预期行为。
Linux 保留目录 0700/文件 0600、同目录替换与目录 fsync，替换前保持目标文件权限。

### 单节点准入和调用生命周期

所有 Store/Resolver 实例共享按 environment/credential_id 划分的进程内屏障，
授权、审计、解密和租约登记与状态提交有确定顺序；只登记非秘密身份、版本和取消状态。
禁用/撤销提交后拒绝新调用并取消本进程在途租约；替换使普通旧版本租约失效。
WithCredential/WithCredentialContext 只能使用当前版本。历史版本必须经独立服务端
HistoricalAuthorizer 核对不可变引用及身份；默认禁止，当前没有任何 HTTP/任务入口安装历史授权。
不得用客户端传入 task_id 或 version 直接授予历史访问。

渠道租约覆盖整个请求，实际 HTTP 使用租约上下文；连接测试同样传递取消上下文。
完成和重试释放租约、恢复父上下文并清空渠道 Key 上下文，包括从受控渠道重试到旧渠道。
解密内容仅在请求内存中短暂使用，Go string/HTTP header 无法保证立即物理擦除。
本批只允许已迁移 OpenAI 渠道的 /v1/chat/completions；其他协议、异步任务、旧余额/探测、
复制、Key 查看和未验证的管理操作明确拒绝。混合批量操作在可能触及受控渠道时整体拒绝，
以免部分成功；legacy 单渠道路径保留原有行为。

共享缓存中受控渠道 Key/Keys 为空，仅轮询索引、slot 数量/状态等非秘密元数据参与同步。
连续请求按共享索引轮询，跳过禁用 slot，刷新缓存保留索引，不把解密副本写回缓存。

限制：只保证单应用进程经 Store 进行的写入，不支持多实例广播/直接 SQL 写入；
取消不能撤回已经发送的字节，也不能撤销供应商已发生费用。任务、OAuth、其他供应商与全量
迁移仍未验收，真实密钥继续禁止接入。数据库与 ring 仍须独立备份，旧 ring 恢复后先轮换 KEK。

### 可复现验证

- Windows Go 1.26.10：ACL/替换用例三轮；真实登录 JWT+权限管理路由、跨 owner 拒绝、
  多 Key 连续轮询/缓存刷新/撤销，以及当前/历史准入与并发撤销均通过。
- SUANJIE_TEST_DATABASES=local-synthetic 时，TestStoreDatabaseMatrix 使用本轮独立的新测试库：
  SQLite 3.50.4、MySQL 8.4.13（127.0.0.1:23316）、PostgreSQL 17.11（127.0.0.1:25436）。
  三引擎各三轮，包含独立连接池 CAS、唯一约束与审计事务回滚、版本/撤销和 Rewrap。
  未设置该开关时 MySQL/PostgreSQL 显式 skip，不能算三引擎通过。
- 最终完整 router/middleware 回归、根模块 go vet ./...、go build ./... 通过。
- 本机 Docker Linux amd64 / Go 1.26.10：无网络、过滤源码只读挂载，完整
  credentialcrypto/credentials 用例三轮（SQLite；MySQL/PostgreSQL 明确 skip），
  完整 router/middleware 回归，以及受控渠道/凭据管理定向测试通过。这是本机 Linux 测试，
  不是远端 CI，也不是 Linux 三引擎矩阵或真实服务器部署验收。
- 广泛后端回归未全绿：Windows SQLite audit.db 清理占用造成大量清理失败；在干净
  product/v1 47f3f7b 副本中重跑 AuditDatabaseMatrix 和 AdminUserRoutineEditsDoNotRequireProof
  同样失败。另有已在基线复现的 affinity 计数与 HTTP/2 GOAWAY 本机失败，未修改无关业务。
  首轮发现的旧视频路由回归已收窄为仅受控渠道拒绝，并以完整 router/middleware 重跑通过。
- 新代码未推送，未运行对应 GitHub CI；远端 a38a817 的 CI 成功不能替代本批验收。
- 不把仓储矩阵当作 MySQL/PostgreSQL 历史渠道迁移/恢复、最低支持版本、分布式撤销、
  跨账户 Windows ACL 攻击演练或真实供应商验收。前端本批无改动，未重跑。

安全参考：OWASP Authentication、Session Management、Key Management Cheat Sheets；
采用服务端权限验证、身份/用途绑定、最小权限密钥文件与失败关闭，不宣称完整 ASVS 合规。
