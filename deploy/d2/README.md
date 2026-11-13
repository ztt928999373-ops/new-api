# D2 自有 GHCR 手动发布

仅发布 `ghcr.io/ztt928999373-ops/suanjie-api`，不是部署任务。复用 D1 根 Dockerfile，D1 的 Compose、测试镜像和业务代码均不改。首批为 Linux amd64，不承诺多架构发布。没有 latest、自动标签发布或官方 Docker Hub 推送。

## 使用前由所有者配置

1. 人工审核并合并本 Draft 到 product/v1。首次开发核实该分支默认 SHA 为 `5be2a57c4343f795a4a89f52d161ed91d84afca7`，#30 已合并；该精确 SHA 的安全回归 43417340771 成功。复用 D1 动态验收 43415247831 的证据，不重新跑容器全量或业务全量测试。
2. 仓库 Settings → Environments 创建 `d2-ghcr-release`，配置至少一个真实人工 Required reviewer。可由所有者本人审批，但不能使用机器人账户冒充审核。建议禁止管理员绕过，并设置 Selected branches 仅允许 product/v1。本轮只读检查发现环境尚不存在；没有代用户创建或修改设置。
3. 工作流会检查审批规则确实存在；缺环境、无人工审批规则、权限不足均拒绝发布。环境名称本身不构成保护。GitHub 环境仅是发布权限审批门，不会创建 VPS 或部署产品。
4. 仓库允许该工作流使用 GitHub 提供的 GITHUB_TOKEN。资格任务仅 contents:read/actions:read；审批后的发布任务增加 packages:write，不申请 PAT、id-token 或长期 registry 密钥。若包已存在，需给本仓库授予该包 Actions 写权限；不要为绕过权限问题添加个人长期密钥。

## 人工发布流程

1. 确认当前 product/v1 的完整 40 位 SHA 已人工审核，并等待该 SHA 最新一次 push 安全回归成功。当前发布仅接受当前 tip，短 SHA、历史 SHA、其他分支和 pending/skipped/cancelled 均拒绝。
2. GitHub Actions 选择“D2 算介自有 GHCR 手动发布” → Run workflow，分支必须 product/v1，sha 输入该完整 SHA。
3. 资格任务校验产品仓库/分支/事件/事件 SHA/实时分支 SHA、人工审批环境及安全回归。此时没有 packages:write。
4. 人工在 `d2-ghcr-release` 审批确切 SHA。审批后再次校验，避免等待期间主线或 CI 改变；GHCR 已存在相同 SHA 标签则拒绝覆盖，认证失败不得冒充标签不存在。
5. 以根 Dockerfile 构建；验证 OCI source/revision/version/license、Linux amd64、原始入口、无内置秘密，核对三个许可证文件原文哈希。不启动应用、不访问数据库、不运行 D1 测试层。
6. 登录仅 ghcr.io，推送 `sha-<完整SHA>`，摘要写入 Actions Summary，退出 registry 登录。不推送其他标签、其他仓库或任何镜像到 Docker Hub。
7. 未来部署及回滚选择 Summary 中的 `ghcr.io/ztt928999373-ops/suanjie-api@sha256:...`。完整 SHA 标签不是 registry 强制不可变保证，digest 才是内容标识。该任务不执行服务器部署、注册或充值设置变更。

## 权限、成本与边界

所有 action 固定完整 SHA；所有输入经严格 SHA 校验，仅使用可信产品提交，不执行 PR head 的特权任务。PR 专项仅 Node Mock 测试，无 packages:write、无 Secrets、无真实 GHCR 写入。registry 访问仅固定 ghcr.io，超时 15 秒、禁止重定向，只有已认证 HEAD 404 才认为标签可创建。发布任务互斥，不取消正在运行的发布；另一个管理员在检查后写同标签的外部竞争仍属于仓库/包管理边界。

GITHUB_TOKEN 与短期 GHCR bearer 不输出、不持久化、不进入构建参数或镜像；Docker 管理员和仓库管理员的固有权限不能由工作流消除。禁止公开完整 registry 配置。审批环境保护被管理员删除/绕过、仓库/包写权限被滥用仍是管理风险。

无付费 AI、无 VPS 购买；每次人工发布构建一次，消耗 GitHub Actions、GHCR 存储及网络额度，是否收费取决于仓库/包可见性和账户策略。不会自动更改包的公开/私有状态。实际 GHCR 权限、首次 token 授权、真实推送、digest 获取需经人工授权的首次发布验证；本轮没有真实推送，不声称已经端到端发布成功。

基础镜像 digest 与构建流程沿用 D1；apt 等上游资源可能变化，因此不能仅以源码 SHA 宣称字节级可重现。保留 AGPL/NOTICE、源代码链接与第三方清单；完整传递依赖及系统包许可证审查仍是正式发行前工作。业务内 VERSION 不被本任务改写，OCI 版本采用完整 SHA 标记。

验收：`node --test .github/automation/ghcr-release.test.cjs`，`node --check .github/automation/ghcr-release.cjs`，以及 actionlint 检查两个 v1-d2 工作流。本批不触发发布、不运行无关 Go/前端/三数据库全量，也不进入 C2。
