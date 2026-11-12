# 商业版 V1 功能盘点（静态初查）

日期：2026-11-12。证据基线：77d5158c297a53e3f21f6082895d3509e592fbaa，product/v1。
此为代码入口盘点及下一阶段验收计划，不代表线上配置、资金正确性或商用上线验收完成。没有访问生产库，没有开启注册、充值或支付。费用管理后台 #22 由另一会话负责，本分支不修改。

| 能力 | 已见代码证据 | 尚待验证 / 下一步 |
|---|---|---|
| 客户注册 | router/api-router.go /user/register；controller/user.go Register 检查 RegisterEnabled、PasswordRegisterEnabled，包含输入及邮件验证分支 | common/constants.go 两个源码默认值均为 true，不能据此前会话“注册关闭”断言任何新实例也关闭。需独立核实持久化配置与启动策略；只做封闭合成用户验收。邮件验证、滥用限制、禁用后的 UI 一致性待验收 |
| 充值订单 | model/topup.go TopUp、用户/管理查询、ManualCompleteTopUp；router 有 topup 与 admin complete 入口 | 明确单位/币种/报价快照、订单状态机、超时与人工补单权限；并发及重试跨三数据库验收，不直接重新开发一套订单 |
| 钱包 | model/user.go 用户额度；model/topup.go creditTopUpQuota 使用容量检查与事务内入账 | 这是额度余额体系，不能直接等同独立人民币钱包或完整双分录资金账本。需梳理充值→额度→消费→退款→调整全链路与审计；核对缓存/数据库一致性和重启恢复 |
| 支付通道 | controller/topup_stripe.go、topup_creem.go、topup_waffo_pancake.go 及易支付入口；payment_webhook_availability.go 有配置与合规门槛 | 首批只选一个沙箱通道；验签、金额币种绑定、事件重放、乱序、多次回调、退款拒付、故障重试、对账均需独立证据。发现 Stripe 验签调用不等于完整验证通过 |
| 入账幂等 | model/topup.go RechargeEpay 事务、订单行锁、provider/status 检查、成功状态短路，额度严格换算 | 仅代码观察；SQLite/MySQL/PostgreSQL 并发重复、无效金额、错 provider、失败回滚、重复通知等需实际执行；不宣称三库通过 |
| 支付启用 | payment_compliance.go requirePaymentCompliance；payment_webhook_availability.go 通道凭据检查 | 门槛存在不代表所有入口已闭环。保持未启用，后续逐入口核实；代理不确认商业合规、不填写真实商户凭据 |

## 分批顺序与交付门槛

1. 先审核合并自动化机制；用合成 PR 验证 Codex 原生 Automatic Code Review 与 Telegram 送达。用户保留最终合并权。
2. 商业 V1 需求冻结：注册方式、客户可见计价单位、钱包是否仅额度、首个沙箱通道、退款及对账责任。现有费用后台继续独立推进。
3. 注册/订单/钱包/回调各自建立验收矩阵与可追溯证据；优先补测试再评估缺口；每项代码独立分支 Draft PR。涉及认证先按 AGENTS.md 阅读并映射 OWASP ASVS/相关 Cheat Sheets，资金相关遵循 .agents/rules/billing.md。
4. 三数据库资金不变量、禁用开关、沙箱支付及回调恢复全部验收后，单列商用风险清单交用户决定。沙箱通过也不授权真实支付或生产部署。

## 当前优先风险

- 自动化待合并与配置：本地测试不证明 Codex 集成、Actions 权限及 Telegram 外部投递已生效。
- 默认分支对 schedule/workflow_dispatch 的限制待核实；不通过修改默认分支绕过唯一主线要求。
- 注册源码默认开启与实例关闭是不同事实；未检查实例持久化配置。
- 资金代码存在不等于商业资金验收通过；退款/对账/独立账本是待确认范围，暂不判断为代码缺陷。
- 既有 #19 的详细验收描述属于历史 PR 自述，本次未重跑，不复用为本分支验证结果。
