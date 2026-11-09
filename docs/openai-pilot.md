# 算介 API V1：OpenAI 单模型试点

本试点复用 New API 的 Channel、OpenAI Adaptor、TokenAuth、分发、限流和结算。
不改动 `/api/pricing`、公开模型目录、注册、支付或充值设置，不进行公网部署。

## 准备入口

`POST /api/channel/openai-integration`，使用现有管理员会话或有对应权限的管理凭据，
要求 `channel.sensitive_write`（以项目现有权限定义为准）。JSON 字段为 `name`、
`model`、`key`。不要通过聊天、命令行参数或版本库提供真实 Key。

入口固定 OpenAI 官方地址 `https://api.openai.com`、类型 OpenAI、单模型、
`openai-pilot` 分组、手动禁用状态。请求中的地址、状态、分组和覆盖参数均不会生效。
保存通过已配置的凭据仓储，在同一事务中创建加密凭据、渠道引用及能力记录，响应仅返回渠道 ID。

本入口要求 PR #5 的 CredentialResolver/Store 已配置；不可用时失败关闭。
渠道 Key 保持空值，供应商凭据进入信封加密仓储，以 CredentialRef 绑定。
请求级解析和租约沿用 PR #5；普通和流式调用均不将明文写回渠道或共享缓存。
受控渠道的旧 Key 查看、复制及不支持的管理路径继续拒绝。
公开页面与普通调用客户端不能取得供应商密钥。

## 调用及人工启用

准备完成后，由管理员核对模型授权、有效售价、供应商预算、试点用户组和平台 Token 的模型限制。
通过现有渠道状态管理接口启用渠道。仅给试点用户分配该组，不开放注册或充值。
调用现有 `/v1/chat/completions`，分别验证 `stream:false` 与 `stream:true`，
流式请求携带 `stream_options.include_usage:true`。客户端使用平台凭据，不使用供应商 Key。
未提供真实 Key 时不启用、不修改目录为 supplier_verified、不把 Mock 当作供应商证据。

## 验证边界

新增 Mock 覆盖真实 Adaptor HTTP 传输、上游认证头与客户端认证头隔离、
普通响应及 SSE 内容、上游 Usage 提取、401/429/500 错误解析；
准备入口验证禁用状态、固定地址、单模型限制和不回显 Key。
另有 router/openai_pilot_e2e_test.go 通过真实 TokenAuth、渠道分发、Relay、Adapter 和隔离数据库，
验证 Usage、钱包/有限 Token 配额与消费日志一致性、500 后重试只结算一次、失败/超时异步退款、
流中断不重放及余额不足/非法 Token 不访问上游。它采用合成供应商与按次售价，不能当作真实账单对账。
既有结算与重试回归仍需通过 CI，供应商成本对账不在本次新增功能中。

真实验收待具备：专用低额度供应商账号、获授权模型、服务端录入凭据、
受限试点 Token、出口 TLS、有效售价和明确费用上限。
记录普通/流式输出、请求及上游请求 ID、实际 Usage、消费日志、取消/限流/失败结果，
并核对平台结算和供应商账单。流中断的 Usage 可能不完整，不能宣称估算为实际值。
未通过前保持人工审核的 Draft PR，不自动合并。

## 安全依据

参考 OWASP Authentication、Session Management 与 Secrets Management Cheat Sheets：
服务端授权、最小权限、敏感值不入响应/审计、TLS、轮换和数据库访问隔离。
不宣称完整 ASVS 合规认证；真实供应商、跨节点撤销及完整结算验收仍待专项验证。

- https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html
- https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html
- https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html
