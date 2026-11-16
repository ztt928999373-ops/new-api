# 算介 API｜V1 放行关卡与证据清单

> 仅记录验收原则与可追溯证据，不执行生产部署、真实 API、支付或其他动态操作。对应 [三阶段路线图](./roadmap-v1-v15-v2.md)。

## 判定规则

每个关卡的状态只能是 `PASS`、`FAIL`、`SKIPPED`、`NOT_RUN`、`UNKNOWN`。`SKIPPED`、`NOT_RUN`、`UNKNOWN` 均不能替代 `PASS`。旧 PR 中的成功不能自动证明最新 `product/v1` SHA 通过；记录工作流/Job 的实际 SHA、Run 链接和 Artifact 摘要。

### 核查顺序

| 关卡 | 最小验收证据 | 当前已知状态（G0 更新于 2026-11-16，其余保留原审阅边界） | 放行要求 |
| --- | --- | --- | --- |
| G0.1 最新主线身份 | 读取当前 `product/v1` SHA，区分 PR head、合并 SHA、镜像源码 SHA | 本次核验实时主线 7dc9da0372b7dbd3ebc6a273346523219680ff8e；镜像 Revision 单独保留 | 当次一致、无未经审查的主线移动 |
| G0.2 关键 CI | 同 SHA 的前后端测试、三数据库安全回归、依赖/发布契约（如适用）；单独记录每个 job 成功/失败/跳过 | 上述 SHA 的 [CI](https://github.com/ztt928999373-ops/new-api/actions/runs/43731233824)、[三数据库](https://github.com/ztt928999373-ops/new-api/actions/runs/43731233856)、[D2 专项](https://github.com/ztt928999373-ops/new-api/actions/runs/43731233906) 均 success；不沿用到未来 SHA | 所有被要求的 Job 实际完成成功，证据可查 |
| G0.3 D3 Preflight | GitHub Actions Run 43726606723 / Artifact d3-43726606723-1 | PASS：仅预检，`preflight_pass`；`limits_verified=false` | 仅允许进入动态验收准备 |
| G0.4 D3 Full | 所有者单独批准；受控 Runner 下 13 阶段、真实容器限额、SQLite 卷权限、冷备份、全新卷恢复、持久化、回滚、清理、Job 和 Artifact | PASS：[Run 43733570539](https://github.com/ztt928999373-ops/new-api/actions/runs/43733570539)，13 阶段 success、limits_verified=true、outcome=d3_pass；原始 ZIP/报告校验通过，[核验归档](../../deploy/d3/acceptance/run-43733570539/README.md)；结项记录待所有者审阅，不等于生产放行 | 13 阶段全成功、`limits_verified=true`、`outcome=d3_pass`、Job/Artifact 成功，无 UNKNOWN |
| G1.1 模型 API | 明确支持的文本模型、能力/地区/价格来源、渠道禁用、Mock 成功与失败、超时、SSE、usage、故障切换 | 已有 #2/#3/#4/#9/#10 等历史成果 | 当前主线测试 + 实际页面/API 证据，未覆盖场景要明示 |
| G1.2 密钥及授权 | Root/管理员/普通用户/匿名，凭据加密/轮换/撤销、禁用零出站、旧明文迁移边界、TLS 限制 | 已有 #5/#6/#11/#12/#21；#21 仅首批 OpenAI 范围 | 各支持渠道无明文回退、权限矩阵无回归、历史迁移风险已处置或隔离 |
| G1.3 费用、路由与预算 | 钱包/Token/消费日志一致；供应商责任预留/冻结/核销；失败/超时/重试不会重复或无授权扣费 | #13～#18、#22、#28 提供合成测试证据 | 运行支持数据库的回归，账单来源/未知费用明确区分 |
| G1.4 客户使用闭环 | 合成客户注册/登录、钱包、API Key、Mock 调用、拒绝、审计与租户隔离；公开注册关闭 | #28 已合并 | 浏览器/API 关键路径重验，P0 问题为零 |
| G1.5 旧 PR 整理 | #1 与 #19 对照主线，重复/冲突/遗留价值标注，决定保留或关闭 | 两者仍为开放 Draft；未合并 | 不在未经复核时直接合并旧代码 |
| G2.1 供应商授权 | 官方服务条款、商业/转售许可、地区及数据处理、账户预算和身份 | 未有真实授权与付费调用的本次验收 | 独立核查与所有者批准，否则禁用 |
| G2.2 实际供应商费用 | 极小额度真实模型调用的 usage 与账单核对、成本区间、错误/超时和异常冻结 | NOT_RUN：既有 Mock 不代替真实对账 | 实测记录与真实账单一致；无法核对则不开放商业调用 |
| G3.1 支付与财务 | 合法收款方案、支付沙箱、签名验证、重复回调幂等、退款/撤销、收入与成本分离 | 未取得 C2 实际沙箱通过证据 | 独立许可与沙箱验收；实际收款单独授权 |
| G3.2 服务运维 | HTTPS、Origin、防滥用、密钥备份/恢复/轮换、监控告警、存储、故障恢复演练 | D1/D2 不证明生产验收 | 选定环境动态验收；有可执行的灾备及事件响应预案 |
| G3.3 商用合规和安全 | AGPLv3/附加条款、NOTICE、源码提供义务；上游转售/授权、隐私、支付、监管要求 | 部分文档/许可已在仓库；尚未完成商业合规核查 | 正式服务前逐项得到结论，无阻断项 |
| G3.4 小规模试点 | 真实但获授权的试点客户、可撤销访问、费用上限、支持、可追踪日志和服务指标 | NOT_RUN | 在授权边界内完成封闭试点并复盘 |

## 质量证据模板（每次执行复制）

- 检查日期（含时区）：
- GitHub 仓库与主线：
- 完整源码 SHA：
- 运行/Job/Artifact 链接：
- 执行环境、数据库版本与模型：
- 测试命令与作用范围：
- PASS / FAIL / SKIPPED / NOT_RUN / UNKNOWN 数量：
- P0/P1 缺陷编号、复现、修复及回归：
- 未验证项、原因与风险：
- 安全/许可/费用检查：
- 是否发生真实支出、部署、密钥使用：
- 人工审查与审批记录（由所有者填写）：
- 结论：仅针对具体关卡，不等于整个产品正式上线

## 对于下一轮 Codex 的执行说明

1. 只读获取实时主线 SHA、最新检查、开放 PR 和已合并 #20/#21/#22/#28 的现状；不沿用老的缺口清单。
2. 优先完成 G0.2 的同 SHA 核查；若失败或证据不足，先提交独立只读诊断，不重跑 D3 或强行将状态标记为成功。
3. G0.4 D3 full 只在所有者明确批准时执行；预检通过不是授权。不能使用过时 expected_sha。
4. 形成 G1 的新缺口表：只报告能在当前 SHA 复现的问题；每个独立 P0/P1 最小 Draft PR，先加失败测试，后修复，并给出安全与回滚说明。
5. 所有 PR 不自动合并，不修改公开注册、正式支付、生产配置或调用付费供应商；若有新的外部成本或部署动作，另请明确授权。
6. 完成 V1 后再研究 V1.5 遥测与影子路由，V2 仅在用户需求和成本得到验证后启动。

## 阶段晋级签署

- V1 → V1.5：G0/G1 的强制项 PASS，真实供应商成本/合规问题有明确受控解法；运营指标能被测量。由所有者批准。
- V1.5 → V2：智能路由已与固定优先级对照，策略可禁用/回滚，性能或成本改善有真实证据，且无预算/成功率安全回退问题。由所有者批准。
- V2 图像 → 视频 → Agent：按用户需求、运营能力和成本上限逐个审批，不凭路线图自动启动。

## P1-02d 受管百炼成本准入证据（2026-11-16）

基线 `b0d3a0fe6a395b880f8d475f71c6077e5ad02c0c`：PR #48 合并后
[前后端 CI](https://github.com/ztt928999373-ops/new-api/actions/runs/43834766455) 与
[三数据库安全回归](https://github.com/ztt928999373-ops/new-api/actions/runs/43834766471) PASS。
本地渠道配置/权限、TaskPlugin 校验、最终 HTTP/WS 状态保护定向 race PASS，0 FAIL / SKIPPED。
完整后端 race 本轮 NOT_RUN；controller、pkg/perf_metrics、relay/channel/gemini、
relay/channel/minimax、service 的五个既有失败包仍记 FAIL，不声称全绿。

审查：百炼准备入口建立禁用的固定北京地址、单模型受管渠道，但预算标记默认为 false；
人工启用后，BeginUpstreamBudget 在此标记为 false 时直接放行。
预算配置 controller/model 及已标记渠道的最终 HTTP 准入只支持 OpenAI，不能配置可用百炼策略。
标记为 true 的百炼请求会被拒绝，此处不是已启用百炼预算策略的绕过。

修复前复现：`SUANJIE_TEST_DATABASES=local-synthetic go test -json -race -count=1 ./router -run '^TestUpstreamBudgetBailianAdmission$'` FAIL。
SQLite/MySQL/PostgreSQL 中，`managed_no_budget` 和 `managed_declared_ceiling_without_marker`
各观察到 1 次 Mock HTTP 出站且无预留；`managed_declared_ceiling_with_marker` 为 0 次；Legacy 对照为 1 次。
HTTP transport 完全替换为本地函数，无真实供应商访问。另有真实 TokenAuth→Relay→Adaptor 的
`TestBailianPilotAuthenticatedSettlement/ordinary` 基线 PASS，证明现有受管路径确实允许调用。
最终 WS 边界使用 loopback 拒绝握手计数，受管百炼也应在握手前阻断。

成本边界：客户售价、测试中的合成价格、令牌 Usage、管理员声明的单次额度及 cost_basis 的哈希
都不是供应商真实账单或经验证的成本上界。现有预留是美元微单位保守责任，与用户钱包独立；
Finish 按声明上限分配责任，错误/崩溃保留 held，不靠退款释放责任。
结构化供应商账单仍是受信操作员的观察输入，并非独立拉取验证；现有 identity 固定 OpenAI/USD，
不应直接当作百炼人民币/地区/阶梯/缓存成本。仓库中没有已核实的该百炼模型供应商成本上界及账户级硬限额。

最小修复方向：在最终 HTTP 和 WS 发送前，根据数据库真实 type 与 CredentialRef，拒绝受管百炼。
不可凭客户端上下文、旧选择类型、false 标记或合成声明恢复准入；不扩展现有 OpenAI 预算白名单，
不修改价格、用户钱包、收费规则或 Legacy 行为。兼容性代价是受管百炼试点暂停普通及流式调用；
安全成本依据、货币/地区/模型、保守估算、额度与并发责任预留均经独立专项验证后，才可另行设计开放路径。
Legacy 百炼仍没有本专项新增的预算承诺，不得把它当作绕开受管门禁的运营方案。
