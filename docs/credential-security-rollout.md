# 算介 API：供应商秘密管理分阶段实施与恢复

本文件保留基础阶段的盘点与设计；后续仓储、受限渠道迁移和管理员接口的实际进度、
测试范围及尚未完成项，以 credential-channel-migration.md 为准，不将设计清单当作已验收功能。

## 当前交付边界

独立分支基于 product/v1 47f3f7beb，不包含或改写 OpenAI Draft PR #4。
此阶段交付源码秘密面盘点、信封组件、LocalFileKeyProvider 和授权 Resolver 基础。
组件尚未绑定任何现有 API、数据库或 Adapter；不会自动加密历史渠道，也不会生成生产 KEK。
本阶段不是完整密钥管理产品验收，后续迁移、用户/管理 API、界面和任务兼容必须分别审查。

## 秘密面盘点与处理策略

盘点的是源码路径，不读取 .env、生产数据库或凭据文件。秘密字段按“内容是否可能承载秘密”分类，
不能仅以字段名含 key 判断（插件 key、artifact key、模型 ID 通常是标识符）。

| 秘密面 | 实际入口/读写路径 | 迁移策略 |
| --- | --- | --- |
| Channel.Key 单 Key | model/channel.go Insert/Update/BatchInsertChannels；controller/channel.go 新增、编辑、复制 | 单 slot 独立 credential/version；存库不保留明文 |
| 多 Key | GetKeys/GetNextEnabledKey；ChannelInfo 索引、轮询、禁用；controller/channel.go 追加、删除 | 每 slot 独立凭据，保留原顺序及状态并映射稳定 slot ID；不能把密文按换行拆分 |
| OAuth 包 | service/codex_credential_refresh.go；controller/codex_usage.go 直接 Update("key") | access/refresh/id token 连同相关敏感字段组成秘密包；刷新生成新 DEK/版本，CAS 更新、刷新租约 |
| Vertex 服务账号 JSON | relay/channel/vertex/adaptor.go 从 ApiKey 解析；多 Key JSON 数组 | 每个服务账号完整秘密包，解析在请求边界，私钥不得拆入公开 DTO |
| AWS AK/SK/API Key | relay/channel/aws/adaptor.go 从 ApiKey 解析多段凭据 | 整包加密，保持原适配语义；区域等公开配置分离 |
| HeaderOverride | model/channel.go GetHeaderOverride；relay/channel/api_request.go 覆盖头 | Authorization、API Key、自定义秘密头保存引用；保留非秘密模板，禁止回显/导出真实头 |
| Other/Settings/OtherSettings/ParamOverride | 各供应商及插件动态读取；relaykit/dto/channel_settings.go | schema 分类；已知秘密字段迁移。未知/自定义任意字段必须先人工分类，或整份配置按秘密处理；未分类渠道不得标记迁移完成 |
| BaseURL/Proxy/Organization/Remark 等 | 渠道管理、HTTP 客户端、余额/测试 | URL userinfo/query、代理口令视作秘密；生产拒绝 credential-bearing URL 或抽取秘密引用；名称/备注不得用于拼接 Key |
| 全局供应商/插件配置 | options、任务插件参数、环境注入 | 单独建立 allowlist/secret manifest；非供应商支付、登录秘密不在本次迁移中，不得宣称全系统秘密均已加密 |
| 渠道缓存 | model/channel_cache.go DB.Find、CacheGetChannel；model/ability.go 批量读取 | 缓存元数据/引用，去除长生命周期明文 Keys；秘密缓存单独有界，不能用 TTL 代替撤销 |
| 任务私有数据 | model/task.go InitTask/Value；service/task_polling.go；controller/task.go；任务 jsplugin adaptor | 替换 PrivateData.Key 为 credential ID＋固定版本；旧任务保留提交身份，不能自动切到最新 Key |
| 请求运行态 | middleware/distributor.go、RelayInfo.ApiKey、Adapter | 通过 Resolver 注入请求级明文；禁止直接读密文或自行解密 |
| 原始请求/响应、错误 | Adapter debug、HTTP 错误、掩码、审计、SQL 日志 | 不记录秘密包/认证头/原始凭据报文；安全错误码；审计只记录 ID、actor、动作及结果；现有正则不是任意秘密过滤器 |
| 导入导出/备份/恢复 | 渠道批量入口、数据库快照、任务表、WAL/binlog | 受控明文导入仅在后端加密；默认禁止明文导出；历史备份视作明文秘密资产 |

## 格式与 Local Provider

每个内容版本生成随机 256-bit DEK，仅 Seal 一次。AES-256-GCM，nonce 和附加 tag
保存在 envelope 中，KEK 仅包装 DEK。AAD 按固定长度前缀编码绑定 environment、tenant、
owner type/ID、credential ID、供应商、凭据类型、用途和版本；格式域固定 v1。
替换内容创建新 DEK，Rewrap 只改变 wrapped DEK，不改变内容密文。

KeyProvider 支持 EncryptDEK、DecryptDEK、RotateKEK、RewrapDEK、GetKeyVersion。
未来 KMS/Vault 仅新增 Provider，不在本阶段接入。跨 Provider 迁移由受控迁移器从源解包后
向目标重新包装；不能将 LocalFile 的 key version 当作云 KMS 的资源 ID。

Local Provider 为单主机、单一规范路径设计；包装 nonce 使用 KEK 独立单调计数，
文件锁→重新加载→持久化预留→包装。失败不得输出密文，计数达到上限要求轮换。
Unix 原子替换后 fsync 目录；Windows 使用 MoveFileEx WRITE_THROUGH。
存储设备及文件系统必须支持这些语义；不支持的网络文件系统不予生产启用。
锁竞争 fail-closed，不绕过锁；进程崩溃遗留锁必须由运维确认无写入者后清理。
不得复制同一可写 ring 到多个节点；恢复旧快照必须先生成新 KEK 再包装，防止计数回退。
快照回滚不能仅靠本地文件自行检测，正式恢复流程/工具是生产启用前置条件。

KEK 文件独立于数据库、仓库和镜像。Unix 限制 0600，可信目录 0700；Windows 必须额外配置
NTFS ACL（Go 文件 mode 不能代替 ACL）。生产/开发/测试独立环境与 KEK。
运行时确实需要受控内存中的明文；clear 字节缓冲不能保证 Go 字符串/GC 副本彻底清零。

## 最简数据库与权限设计（待后续 PR 实施）

核心采用 credentials（身份/owner/状态/当前版本/revision）、credential_versions
（不可变密文与 wrapped DEK、nonce、format/provider/KEK 版本）两表；channel bindings
以 channel ID、slot ID 关联，任务只存 credential/version 引用。第一版不需要完整企业租户系统。
独立 wrapping 表仅在需要并存多个 Provider 包装时引入，当前使用版本化 envelope 即可。
metadata 不返回秘密；last_used_at 和 error_code 为非敏感状态，禁止原样存上游错误。

BYOK owner 从服务器认证上下文生成，禁止请求体覆盖；平台凭据单独授权。
管理员权限不能越过 BYOK owner 边界。当前基础 Resolver 对平台操作员仅允许平台凭据，
普通用户调用平台渠道后续需经渠道授权生成内部受限调用身份，不能直接授予 PlatformOperator。
新建、替换、测试、禁用/撤销、删除仅返回 metadata 和安全尾号；尾号仅对可展示的 API Key，
OAuth/JSON 私钥不从原文末尾截取，使用随机展示标识。默认没有明文查看/批量导出接口。

## 撤销的立即失效语义

撤销事务提交后，所有新解析/新轮询必须拒绝，权威状态不可只缓存 TTL。
生产接入需“准入租约/版本 fence＋in-flight 请求取消登记”：授权与撤销线性化，撤销广播取消
本节点请求并使其他节点失效；不能撤回已经发送给供应商的字节，也不能承诺撤销免除已有费用。
历史任务进入 credential_revoked 状态并显式提示，不静默换 Key 或继续轮询。
当前 Resolver 每次访问仓储，不缓存秘密；最小安全批次补上单进程准入屏障和在途取消，
详见 credential-channel-migration.md。跨节点广播、OAuth/任务路径仍未接入，
因此目前不宣称完成全系统“立即撤销”产品验收。

## 迁移、轮换及恢复手册

1. 部署兼容代码但不开启加密写入，使用合成凭据演练，确认所有秘密面已分类。
2. 维护窗口停止全部旧实例、OAuth 刷新及任务写入；记录待轮询任务和秘密快照映射。
3. 生成独立 KEK，分开备份数据库和加密后的密钥恢复材料；不存同一存储/权限边界。
4. 分批建立身份/版本/绑定；加密后内存解密比对。事务保存密文＋清理旧明文。
   中断按 checkpoint 恢复；只有显式 migration 模式可读取 legacy 明文，不静默回退。
5. 校验所有渠道、slot、OAuth、任务和配置，刷新缓存；未分类/失败项阻止生产放行。
6. 主密钥轮换：增加 KEK→激活新写入版本→逐条 Rewrap/CAS→验证→保留旧版本供旧备份读取。
   供应商 Key 替换是新内容版本/新 DEK；疑似泄露还需在供应商侧撤销。
7. 恢复：关闭外网出口→恢复数据库和匹配 key ring→验证版本覆盖和解密→核对撤销清单→
   轮换为新 KEK（绝不能恢复计数后继续用旧 KEK 包装）→恢复引用/任务状态→Mock 验证→人工放行。
   恢复旧数据库会复活旧状态，必须对照独立审计/撤销清单处理，不自动重启任务。
8. 旧备份/WAL/binlog 可能仍含明文。按保留期隔离并淘汰，不能宣称 UPDATE 后物理残留立即消失。
   KEK 退役前确认依赖它的备份和任务全部过期。删除密文不等于删除所有备份中的秘密。

## 后续交付与验收清单

- [x] 源码秘密面及直接写回路径盘点，标明未知配置处理门槛。
- [x] 基础 Envelope/Local Provider/Resolver 与合成用例。
- [ ] 数据库仓储/CRUD、权威状态与 CAS、完整渠道兼容迁移。
- [ ] 用户/管理员接口与秘密只写前端、受控连接测试。
- [ ] OAuth 并发刷新、多 Key、任务固定版本/撤销取消、缓存兼容。
- [ ] 运维命令、恢复/轮换工具、ACL 检查与多节点前置条件。
- [ ] SQLite/MySQL/PostgreSQL 迁移矩阵与 Windows/Linux 部署恢复演练。
- [ ] 与独立 Draft PR #4 的 Mock 调用/结算集成；人工审查后才能启用。

不宣称完整产品、全部供应商/秘密迁移、数据库矩阵或真实供应商已验收。
安全依据：OWASP Secrets Management、Key Management、Cryptographic Storage Cheat Sheets；
认证与会话沿用 OWASP Authentication、Session Management；不宣称完整 ASVS 合规。


## 第一批日志安全修复（2026-11-08）

本批基于 a38a81786ec9f9de4088c920d1bf8813e50f21f1，仅修改诊断输出和秘密字段登记。
不修改 OAuth 刷新算法/持久化、多 Key 轮询、Task.PrivateData、历史迁移或数据库 schema。

### 日志边界

- 智谱错误格式不再输出原始 Key，记录 provider、event、reason。
- common/logger/relaykit 日志出口统一处理明确认证字段、认证方案、私钥和常见 token；
  请求级额外移除已知渠道凭据、认证包字符串成员、自定义 Header 值及查询值，不保留全局秘密集合。
- 原始请求/响应、流式帧、OpenRouter 企业错误 data、音频 multipart 内容及任意 LogJson
  改为类型/长度/渠道等元数据；Debug 也不允许恢复这些内容。
- HTTP/WebSocket/OAuth transport 错误仅显示安全原因，保留底层 cause 供 errors.Is/As 使用。
  OAuth 请求、刷新 token 处理和数据库写回不在本批改动中。
- 公共 OpenAI 错误封装不再将 OpenRouter 任意 metadata 拼接进消息；公开错误只保留安全标量，
  保留项目 ErrorCode 的类型/序列化兼容性，覆盖绕过 HTTP handler 的其他 Relay 路径。
- RelayErrorHandler 不再因 showBodyWhenFail 或 Debug 回显原始 body；过滤已知秘密，
  删除可能含任意 Header/秘密的 error metadata 和非标复合 code，保留可用的错误类型、状态和安全消息。
- HTTP 访问日志省略所有 query 值，包括未知参数；不修改实际请求 URL。
- 请求 ID 经过日志清理，文本换行转义，避免上游伪造多条日志。

这是“源头省略 + 已知请求秘密移除 + 出口防御”，不是任意文本秘密识别器。
未知自定义响应若在脱离请求/凭据上下文的路径中出现无标记秘密，不能仅靠正则证明安全。
第三方 SDK、GORM/SQL 日志及外围代理访问日志仍须分别核验配置，不能宣称全部日志系统完成验收。

### 秘密字段登记

service/credentials/secret_fields.go 使用准确的命名空间路径，区分供应商、OAuth、第三方秘密、
秘密容器、公开配置和插件标识。未知字段返回 pending_confirmation；格式审查与字段分类分离。
只登记现有受限 OpenAI bundle 格式，不扩大迁移白名单。登记结果不是加密或迁移授权。

### 仍未迁移的秘密面

- 旧 Channel.Key、多 Key/JSON Key 与长期缓存；仅既有受限 OpenAI 引用已接入。
- Codex access/refresh/ID token 的版本化写回与并发刷新。
- Vertex 服务账号、AWS/百度/讯飞/腾讯等复合认证、派生 Token 缓存。
- HeaderOverride/Other/Settings/ParamOverride、自定义 JSON、带凭据 URL/代理。
- 异步任务明文快照、固定凭据版本、并发撤销及在途取消。
- 全局 options、登录/支付/邮件/对象存储等相邻秘密域，以及旧备份/WAL/binlog。

下一批先统一 OAuth 写入入口、刷新租约/CAS 和派生缓存版本，再处理任务凭据固定版本与撤销。
本批不自动开始上述工作，仍禁止真实 Key 接入和全量迁移。

安全参考：OWASP Logging Cheat Sheet 的 Data to exclude、Verification 和 Log Injection；
Authentication/Session Management 的秘密与 token 不进入日志原则。本批不宣称完整 ASVS 合规。
https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html
https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html
https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html

### 本批本地验证记录

- Windows Go 1.26.10：根模块 `go vet ./...`、`go build ./...` 通过。
- relaykit：`GOWORK=off go vet ./...`、`go build ./...`、`go test ./...` 通过。
- 新增智谱/OpenAI/OpenRouter/OAuth/HTTP 错误脱敏、未知字段和正常诊断用例通过；
  同时验证公共错误 metadata 旁路、错误码兼容性、换行和请求 ID。
- 前端：`node node_modules/vitest/vitest.mjs run --pool=threads --maxWorkers=2 --reporter=verbose`，
  209 个文件、2509 个用例全部通过。默认进程池未正常结束后已停止，采用线程池完成相同完整测试集；
  未修改前端代码、配置或依赖。jsdom scrollTo/canvas 和已有 UI 警告不影响最终通过结果。
- 完整后端回归仍存在 Windows HTTP/2 GOAWAY/不可重放及亲和性计数失败；
  均已在未修改的 product/v1（47f3f7bebfff8c422fb55980118390c398060c01）复现，不作为新增回归修复。
  不宣称全套测试、远端 CI 或 Linux 验证已通过。
- 未执行数据库迁移、真实供应商调用或部署。GitHub CLI 未登录，远端 PR 更新与 CI 验证暂停。
