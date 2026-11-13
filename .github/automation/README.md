# 算介 V1 虚拟员工

## U1 官方更新监控（2026-11-13）

新增独立 `v1-upstream-monitor.yml`，每周一北京时间 09:20（UTC 周一 01:20）检查；GitHub 定时可能延迟。此次已只读核实仓库默认分支为 `product/v1`，因此本工作流人工合并后可在默认分支调度。下文旧版“默认分支 main”的说明属于历史验收状态，不代表当前状态。不修改默认分支或产品代码。

通过 GitHub 只读 API 比较固定官方 `QuantumNous/new-api main` 与当次 `product/v1` SHA，复用 `manager.cjs` 的 `notify`；不改变技术主管与运营主管。简体中文报告保存在 Actions Summary，Telegram 仅发送固定分类计数、SHA、报告链接，不发送上游任意标题/正文。分类为标题关键词线索，全部标记待核实，不能当作漏洞判定或迁移结论。每类最多列 20 个官方提交链接（无重要线索时 3 个），其余指向完整比较。

首次扫描产品缺少的全部官方提交；以后同时比较当前产品完整差异与上次成功通知之后的官方增量。上游 SHA 相同不重复发送通知。只从本仓库、本工作流、产品主线的成功 schedule/workflow_dispatch 运行恢复 JSON 检查点，不执行产物代码；通知成功才推进，未变化时续存。检查点为 90 天保留的 Actions Artifact，无密钥、无仓库代码写入。产物失效/删除或超过最近 30 次成功运行的查找窗口可能导致重新初扫；Telegram 已成功但产物保存失败也可能重报，不宣称严格 exactly-once。

任何比较超过 500 个提交、分页不完整、异常检查点、上游历史改写或访问失败均停止并标记待核实，不伪报无更新、不推进状态。Telegram 缺失时仍产出报告但明确未发送，不推进；部分配置或发送失败使工作流失败。网络 API 单次 15 秒、禁用主动重试，工作流限时 5 分钟。仅 contents:read / actions:read，复用现有 Telegram Secrets，不购买资源、不调用付费 AI。

验收：`node --test .github/automation/manager.test.cjs .github/automation/upstream-monitor.test.cjs`；`actionlint -shellcheck= -pyflakes= .github/workflows/v1-upstream-monitor.yml .github/workflows/v1-automation-check.yml`。本地通知全部 Mock，不发送真实 Telegram。人工合并后首次定时/手工运行和真实送达仍需复核；本批不手动触发真实通知。

AI-assisted。唯一主线 product/v1，费用后台 #22 的分支不受本工作影响。最终审核和合并由所有者决定。

## 审核采用 Codex 原生 Automatic Code Review

在 https://chatgpt.com/codex/cloud/settings/general 为此仓库开启 Code review / Automatic reviews。原生服务负责审核；本机器人只读取 GitHub 状态并通知，不评论、不修改代码、不批准、不合并、不部署。

#23 的官方 connector 已完成一次人工触发审核，证据：
https://github.com/ztt928999373-ops/new-api/pull/23#issuecomment-6824561619
这证明原生集成可用，但该记录标为 Manual request，不能证明 Automatic reviews 开关已开启，更不能证明 Actions Bot 的 mention 被支持。此前 AI Manager 在 require 阶段失败、且 PR 为 Draft，没有成功发出的 Bot mention；缺少“Bot 评论→connector 接受→目标 SHA 审核完成”的证据链。为遵守本批仅通知边界并避免双重消耗，已删除 Actions 的 @codex review 发布代码及 issues:write 权限，不再试发评论。保持 Draft，不通过转 ready 强行验证；自动审核开关与正式 ready 触发仍由所有者核验。

## 安全启动与通知

- pull_request_target 仍只 checkout 事件的可信 base.sha，不执行 PR head / merge ref / artifacts / PR 缓存。首次引入时若 base 尚无 manager.cjs，inline bootstrap 输出 not installed 摘要并安全跳过，不表示通知成功；绝不回退 PR head。已存在文件但加载/执行错误则失败，不把依赖缺失当作未安装。
- Telegram 仅发送 PR 编号、事件、SHA、审核状态与报告链接，不发送 diff、正文或客户资料。旧提交审核明确显示 review.commit_id 与“历史提交审核，不适用于当前 head”。报告按每名审核人的最新有效决定计算风险，未撤回请求修改（含旧 SHA）继续计入。
- Secrets 仅使用 TELEGRAM_BOT_TOKEN 和 TELEGRAM_CHAT_ID，在可信通知步骤注入 env，不插值到 shell/JS 源码、不输出值。请求固定 api.telegram.org，禁止重定向；失败日志不打印 URL、响应正文或异常堆栈。没有配置就标记 not-configured，不发送、不虚报成功。
- GitHub token 仅 contents:read、pull-requests:read、checks:read；persist-credentials:false。无仓库写权限、无代码修改/批准/合并/部署入口。Fork review 事件可能没有 secrets，因此可能不发送。
- Telegram 只做单向通知，无命令处理接口。目标由所有者确认，Bot Token 在仓库 Settings → Secrets and variables → Actions 保存，不进入聊天。当前未配置真实 Token，未实测送达。

## 报告和验证

已核实默认分支为 main；product/v1 的 schedule 不会单独启动，workflow_dispatch 的发现也受默认分支限制。不修改默认分支、不向 main 写入。本会话已有每日约 09:00 Asia/Shanghai 的只读进度报告任务；它在会话交付，不冒充 Telegram 送达。

本地：`node --test .github/automation/manager.test.cjs`。安全测试任务在无 secrets、只读 token 的 pull_request 下执行 PR 测试；bootstrap 测试覆盖可信文件不存在及加载异常，不改变特权任务的 checkout 边界。漏洞扫描保持失败阻断，详见 VULNERABILITIES.md；不关闭、不忽略告警。

合并后由所有者使用合成 PR 联调：可信脚本安装、Draft/ready、新 SHA、历史 SHA 审核、拒绝修改风险、未配置 secret、安全失败与真实通知送达。关闭方式：禁用 manager 工作流或撤销 Telegram Token；既有 CI 不受影响。
