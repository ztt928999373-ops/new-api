# OpenAI 试点安全审查与待确认方案

## 已确认的实现

以下旧路径调查保留作为历史记录。本次与已合并 PR #5 整合后，OpenAI 准备接口不再调用
明文 AddChannel 路径，而是在事务中创建加密凭据与渠道引用；Channel.Key 为空。
受控渠道使用 Resolver 准入与请求租约，旧 Key 查看被拒绝。旧明文渠道仍存在，不作全系统无明文声明。

- `model.Channel.Key` 为持久化字符串，BatchInsertChannels 直接 Create；当前路径没有加解密钩子。
- 常规列表和 GetChannelById(false) 使用 Omit("key")；缓存和转发路径需要完整凭据。
- `/api/channel/:id/key` 受 AdminAuth、RootAuth、CriticalRateLimit、DisableCache、SecureVerificationRequired 保护。
  GetChannelKey 记录 channel.key_view（ID、名称），随后返回明文 Key。因此管理员仍可回显密钥。
- 试点准备接口固定官方出口、默认禁用，不回显 Key，不改变上述旧接口权限。
- Relay 错误调用 MaskSensitiveInfo；覆盖上游地址、IP、URL userinfo/query 和 api_key: 模式，
  不是通用秘密检测器。原始响应 debug 日志也不能视为安全日志。真实 Key 禁止在 debug 模式下验收。

## 待人工确认：推荐外部秘密存储

优先选择 Secret Manager 保存密文、KMS 管理加密密钥。数据库保存 secret reference、版本、
非敏感标识；服务端以最小权限工作负载身份读取，短时缓存并轮换，不向前端回显。
渠道新增/更新、复制、多 Key、缓存、导入导出、Root 查看、余额检测、后台探测及任务轮询
都需统一经过 CredentialResolver，不能只修改请求发送处。

若当前部署不能接入 Secret Manager，可采用 KMS 信封加密：每条凭据独立 DEK，AEAD，
随机 nonce，AAD 绑定渠道 ID/版本，数据库仅保留密文、wrapped DEK、key version。
KEK 不得与数据库、备份或前端部署在一起。鉴权失败、KMS 不可用、密文损坏均 fail closed，
禁止回退读取明文或吞掉解密错误。完整磁盘加密只能作为附加保护。

迁移需要双版本读取、明确的逐条迁移状态、写入切换、缓存失效、备份恢复和轮换演练。
不可原地破坏旧 Key；回退凭据引用和旧版本必须有权限与保留期限。
Root 查看接口如何替换为“仅替换/轮换”需单独确认，不在本 PR 静默改变。

## 真实验收门槛

安全存储方案批准、解析路径覆盖、禁止回显策略、秘密日志过滤及测试、轮换/撤销、
备份权限与恢复演练完成后，才允许录入真实供应商 Key。
当前仅使用合成凭据，未读取 .env.local；本入口已使用 PR #5 静态加密，未宣称完整 ASVS 合规。

## Windows SQLite 既有清理失败定位

第二阶段基线 47f3f7beb 中 newAuditTestDatabase 创建文件 SQLite 连接但未登记关闭清理；
verifyAuditJSONStorage 等路径再次 InitDB/InitLogDB 后也可能留下旧连接。
t.TempDir 删除时 Windows 拒绝删除仍被打开的 audit.db，Linux 通常允许 unlink。
此前独立基线运行 TestAuditDatabaseMatrix/sqlite 已复现同样错误；不是本轮路由测试新增失败。
建议独立变更为每次打开/重开得到的 sql.DB 登记 t.Cleanup(Close)，确保关闭先于 TempDir 清理，
并验证新旧数据库、重复 Init 和 LOG_DB/DB 别名。不要只关闭最后一次全局句柄。
原 stash 是相关独立修改，保持原样，不自动恢复或删除。
