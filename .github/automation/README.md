# 算介 V1 虚拟员工

基线：product/v1，77d5158c297a53e3f21f6082895d3509e592fbaa。AI-assisted。
仅新增自动化文件，不修改费用后台 #22、业务开关、数据库或现有验收工作流。用户最终审核和合并；机器人无合并/部署功能。

## 角色与事件

- PR 审核员：非 Draft、目标 product/v1 的新 PR/新 SHA 请求 `@codex review`；按 SHA 去重。Draft 仅通知，ready_for_review 后请求审核。Codex 必须先在 GitHub 集成中启用此仓库 Code review；评论成功不代表服务接受请求或审核完成。GitHub Actions 发出的 mention 能否唤起当前集成需合并后实测；若集成不响应，使用 Codex 原生 Automatic reviews，禁用本工作流请求步骤以免重复。
- 虚拟技术主管：Telegram 单向通知 PR 事件、提交 SHA、审核状态和报告链接；不接收命令，不可合并或启用支付。只发送元数据，不发送代码、diff、PR 正文或客户资料。
- 项目报告员：Actions Job Summary 汇总 product/v1 开放 PR、当前 SHA 的检查状态及审核记录；失败、跳过、未运行单独列出。每日报告 09:00 中国时间（GitHub 可能延迟），也可手动运行。
- 质量岗：新增自动化脚本行为测试；既有 CI 和三数据库安全回归保留。后续漏洞扫描与 actionlint 见本分支工作流。

## 配置与启用

1. 用户审核并合并本 Draft PR 后，配置 Codex GitHub 集成中的仓库 Code review。不要提供 OpenAI API Key；本实现不调用收费推理 API。
2. 在仓库 Settings → Secrets and variables → Actions 保存 TELEGRAM_BOT_TOKEN 和 TELEGRAM_CHAT_ID 两个 Repository Secrets。Token 不进入聊天/源码/日志。目标 chat_id 由所有者确认，私有项目使用受限群。机器人须加入该群并具备发言权限。
3. pull_request_target 只 checkout 事件 base.sha；不 checkout PR head、不执行 PR 文件、不恢复 PR 缓存。脚本仅使用最小 GitHub 权限。fork 的 pull_request_review 通常取不到 secrets，此时会明确标为 not-configured；不视为成功送达。
4. 已核实仓库默认分支为 main。由于默认分支不是 product/v1，GitHub schedule 不会从 product/v1 自动启动，workflow_dispatch 的发现也受默认分支约束。不得为启用日报擅自修改默认分支或向其他分支提交；先由所有者确认触发方式。在默认分支已含该工作流时，只允许手动选择 product/v1。报告步骤始终读取 product/v1。
5. 合并后用合成 PR 验证 Draft 不请求、转 ready 每 SHA 一次、新 SHA 重审、评论审核通知、缺失 secret 跳过、Telegram 真实送达和报告链接。不要把本地 Mock 结果记作线上启用。原生自动审查与 mention 二选一，避免重复。

## 运行边界

- 不修改分支保护、不设置自动批准、不关闭讨论、不自动合并/部署。
- 请求去重依赖 GitHub Bot 评论与 workflow concurrency；重跑会重发 Telegram 消息（方便补送），不保证 exactly-once。
- 报告不下载/执行 PR artifacts，不判断商业化可上线。未配置通知时审核仍可运行，Telegram 返回错误使该次工作流失败且隐藏 token-bearing URL。
- 关闭：禁用 V1 AI review and technical supervisor 工作流；撤销 Telegram token 或删除两个 secrets。现有 CI 不受影响。
