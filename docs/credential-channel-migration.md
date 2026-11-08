# 渠道加密接入：当前范围与迁移手册

## 当前实现

新增 supplier_credentials 与 supplier_credential_versions 两表：身份/owner/状态/当前版本/
revision 和独立的不可变版本密文。仓储提供 Create、Replace、SetStatus、CurrentVersion、
GetForUse，所有替换/状态更新使用 revision CAS；版本唯一冲突必须回滚 head 更新。
新增平台管理员基础接口：POST /api/channel/credentials（新增）、GET /:id（metadata/版本）、
PUT /:id（替换，相当于供应商 Key 轮换）、POST /:id/status（禁用/恢复/撤销）、
POST /:id/test（固定 OpenAI 官方 /v1/models，禁止重定向，只验证认证连通，不验证模型）。
接口复用 ChannelRead/ChannelSensitiveWrite/ChannelOperate，不能读取其他用户 BYOK 秘密，
不返回完整 Key。KEK Rewrap 的数据库批量管理、完备审计管理界面仍需实现。
Resolver 访问审计持久化到 supplier_credential_access_events，管理动作复用原管理审计。

Channel 增加仅服务端可见的 CredentialRef。SetupContextForSelectedChannel 经现有路由授权后
调用 ResolveChannelForRequest；引用存在但 Resolver 未配置、状态不可用或认证失败时返回 503，
绝不回退到旧明文。原 Adapter/Relay 接口不变，明文仅在请求级 Channel 副本/上下文使用，
不写回共享缓存。此过程仍有运行时明文，不能宣称平台无法访问凭据。
目前租户为 default；平台身份只用于已授权的渠道调用，不是用户可自行指定的管理身份。
默认关闭新存储：只有显式配置 SUPPLIER_CREDENTIAL_KEYRING_PATH 和
SUPPLIER_CREDENTIAL_ENVIRONMENT（development/test/production）才初始化存储、元数据表及访问审计。
配置必须指向既有独立 ring；不会生成 KEK 或自动迁移旧 Key。未配置时新管理接口拒绝服务，
已有 legacy 渠道仍走原路径；存在加密引用但无法解析时拒绝调用。
迁移后的 Channel.Update 拒绝明文 Key 替换，需经凭据仓储 Replace。普通 Key 查看只能得到空值。
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
  覆盖 owner 隔离、8 个并发替换仅一次成功、版本唯一约束触发后事务回滚、禁用/恢复/撤销。
- SQLite：实际 VACUUM INTO 备份恢复，缺少备份拒绝、重复迁移、多 Key/缓存副本和撤销拒绝。
- MySQL/PostgreSQL 的历史 Channel 迁移、备份恢复尚未执行；不能从仓储结果推断通过。
- 缓存撤销仅证明每次新解析检查状态；撤销与准入并发 fence、在途取消、任务版本迁移未完成。
- 通用 BYOK HTTP API、OAuth 刷新/任务/全部秘密字段兼容仍未完成，不开放真实密钥接入。
- 管理 API 使用合成凭据＋Mock RoundTripper 测试，没有真实 OpenAI 调用。
- 与 PR #4 的本地集成分支将其 8 场景改为从加密数据库 Resolver 读取，Windows 已通过；
  不代表 GitHub PR 已合并，也未完成异步任务及全部供应商的集成。
- Docker 测试容器仅绑定 127.0.0.1，使用合成密码；不是公网部署。
