# D2 发布安全门禁与 Digest 发布

## 当前流程及最小修复

旧流程：手动 SHA → 仅安全回归 → 环境人工审批 → 构建校验 → 标签 HEAD → 推送标签 → 从终端日志提取 Digest。

新流程：手动完整 SHA → 当前提交 D2 测试 → 三类同 SHA push CI 与全部必需 Job 成功 → 环境人工审批 → 再检查 → 不推送构建/元数据/许可证校验 → 推送前再次检查 → 仅按 Digest 推送、不写标签 → Registry 清单/配置/源码身份验证 → 明确发布状态。

原有根 Dockerfile、D1 Compose、业务、数据库、Secrets、主管与 U1 均不改变。仅为可靠地产生主线同 SHA 证据，通用 CI 和 D2 专项增加 product/v1 push 触发；不使用旧 PR SHA、旧 merge ref 或本机成功冒充主线 GitHub 成功。

## 发布资格

只接受 `product/v1` 当前 tip 的 40 位小写十六进制 SHA，输入必须与手动事件 SHA 和实时产品分支相同。以下三个工作流必须存在当前 SHA 的最新 push 运行，状态 completed/success：

- v1-security.yml：V1 security regression (3 databases)。
- ci.yml：Backend vet, build, and test；Frontend typecheck and test。
- v1-d2-check.yml：test。

还读取最新 attempt 的 Jobs，要求 SHA 相同、必需 Job 恰好存在一次且全部 Job 成功；失败、跳过、取消、未运行、缺失、未知、API 失败和分页不完整都阻断。审批后及推送前重查，避免主线或结果变化。发布前还在资格 job 运行当前提交的 D2 测试。

## 人工审批与权限

发布 job 继续绑定 d2-ghcr-release，只有它有 packages:write；其他任务仅 contents:read/actions:read。缺审批规则或 can_admins_bypass 不为 false 时拒绝。GitHub 平台在审批完成之前不会启动 publish job。本轮只读核实环境：所有者为 Required reviewer、禁止管理员绕过、分支只允许 product/v1，允许所有者审批自己发起的发布（prevent_self_review=false）；本轮不更改该政策或 Secrets。

GITHUB_TOKEN 是唯一写入凭据，无 PAT、长期密钥或 id-token。测试与 YAML 检查不需要写权限。现有 Actions 仓库默认权限 read、不能批准 PR、允许全部 actions、未全局强制 pin SHA；本工作流涉及 actions 自身均已固定完整 SHA，不扩大仓库设置。

## GHCR 版本与防覆盖

目标固定 `ghcr.io/ztt928999373-ops/suanjie-api`。GitHub 官方 Container registry 文档说明按 Digest 拉取，但未找到可作为当前仓库原子不可变标签保证的设置。因此不依赖“先 HEAD 再写标签”的竞态窗口。

本机制使用 BuildKit 官方 `push-by-digest=true` 的 unnamed image 推送，不创建/修改 `sha-...`、latest 或其他标签。原 sha-完整SHA 标签如已存在仍拒绝；检查后若其他写入者创建该标签，本流程也不会覆盖它，因为从不写标签。完整源码 SHA 保存在 OCI revision/version 与发布记录，正式版本引用为 `仓库@sha256:...`。

官方依据：
- https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry
- https://github.com/moby/buildkit#imageregistry

镜像先用同一隔离 BuildKit builder --load 校验，再利用同 builder 缓存进行 Digest 导出推送，不重复编译应用。关闭 provenance 生成额外索引，首批只发布 Linux amd64 单镜像清单。推送后的清单 config.digest 必须等于预先校验的本地 image Id，配置内容哈希、source/revision/version/license/架构/入口也必须匹配；不以 exporter 报出的字符串单独认定成功。

## Digest 验证和状态

读取 exporter metadata 的 containerimage.digest，拒绝缺失或非法格式；从固定 GHCR 地址按该 Digest GET 清单，计算原始字节 SHA256，再获取 config blob 并核对哈希和源码身份。单响应上限 1 MiB，Registry 请求、存储跳转和响应流共用一个 15 秒截止时间。Manifest 禁止任何跳转；仅 Config blob 的 307 允许一次 HTTPS 跳转到精确主机 `pkg-containers.githubusercontent.com`（禁止自定义端口、用户信息、片段及二次跳转）。存储请求使用全新请求头和 `credentials: omit`，不转发 Registry/GitHub Token 或其他认证信息。失败仅记录 `D2_*` 安全码，不输出签名 URL、Token、Registry 正文或原始异常。

状态区分：

| 状态 | 含义与处理 |
|---|---|
| 未执行推送 | 本机尝试标记尚未写入，推送前已阻断；不是对既有历史镜像作不存在声明 |
| 推送命令成功但 Digest 未验证 | 可能已发布，禁止当成未发布或自动重试，人工查询版本与日志 |
| 推送状态未知 | 已开始尝试但命令失败/取消/网络中断或无法取得证据；停止并核查 |
| Registry 已验证发布 | 原始清单哈希、配置及源码 SHA 匹配；仍不代表已部署 |

命令失败但 Registry 可验证时如实记录存在，同时让任务失败，等待人工确认。always() 尽力记录状态；若整个 runner 在推送中被终止而没有报告，也必须按未知处理，不能认为没有镜像。

## 发布及恢复步骤

1. 人工合并修复后，等当前产品完整 SHA 的三类 push CI 全部成功。
2. 在 product/v1 手动触发 D2，输入完整 SHA。审阅资格摘要和源码后人工批准环境；本批不触发此步骤。
3. 构建/许可/元数据通过后，仅推送自有 GHCR；Summary 记录源码 SHA、固定 Digest 和状态。没有 Docker Hub、服务器部署或注册充值操作。
4. 人工对照 Registry digest、发布日志及 OCI revision；对取消、网络错误或记录失败先只读调查，禁止自动覆盖标签或删除历史版本。
5. 指定版本恢复使用 `docker pull ghcr.io/ztt928999373-ops/suanjie-api@sha256:经过记录核实的摘要`；Compose image 也指定完整 digest，复核源码身份后再由独立部署流程批准。不能靠重新构建相同源码恢复字节级相同镜像。
6. 未命名镜像版本须保留，避免仓库清理策略删除无标签版本；请人工核对现有包的保留/可见性/Actions 写权限，不自动改变设置。镜像回滚不能替代停机备份和数据库配对恢复，详见 D1。

## 本轮验证及边界

仅进行 Node Mock、Bun YAML/工作流契约、actionlint、脚本语法及 diff 检查。所有 CI 证据按精确提交区分；真实 GHCR 认证、无标签导出、远端 Digest 联调未执行，不声称端到端发布成功。首次真实发布须用户明确批准，不在本批范围。

未来人工发布仍消耗 Actions/Registry 配额；无付费 AI、服务器购买或 C2。上游 apt 内容可变，不保证字节级可重现；以 Registry 固定 Digest 为准。既有 AGPL/NOTICE/第三方清单原样保留，完整传递依赖审计另行进行。


## 307 修复及既有镜像只读复核

故障运行 [43462940691](https://github.com/ztt928999373-ops/new-api/actions/runs/43462940691) 的推送步骤已经成功；原验证器拒绝 GHCR Config blob 的正常 307，不能据此重推。修复仅涉及验证器、现有测试和本文，不修改发布工作流、权限、业务或数据库。60 项离线测试通过，包括认证头隔离、恶意/降级/连续跳转、摘要/源码错误、1 MiB 限制、超时和安全诊断；测试不代表已用修复代码执行远端验收。

**人工合并修复后**，在云端取包含修复的源码，复用 `verifyRemoteDigest` 读取原镜像；源码 SHA 参数必须仍是原发布 SHA，不可替换为修复提交。公共包允许匿名获取仅 `pull` 作用域凭据，不需要 GITHUB_TOKEN、PAT 或 packages 权限。若访问失败，停止并按安全码调查，不能自动扩权、推送、创建标签或删除版本。写入前的 `assertTagAbsent` 仍要求原有认证，不受匿名复核影响。不新增工作流，也不重跑原 publish Job。

```sh
NODE_USE_ENV_PROXY=1 node -e "require('./.github/automation/ghcr-release.cjs').verifyRemoteDigest({sha:'cd8937c602640fc31c0144994a73266c3d792782',digest:'sha256:725d45a3fe8deb3155950a0935bc9873bf0439ef99a425be2dd5f13cbaa21ddf',configDigest:'sha256:a0cc42d43e45ee9ccf69bc12fad8e7c150df78867817dfc727e155e7b186f66a'}).then(console.log).catch(()=>{process.exitCode=1})"
```

预期输出固定 `ghcr.io/ztt928999373-ops/suanjie-api@sha256:...` 引用并退出 0；失败仅输出安全码并非零退出。保存执行时的修复提交、原源码 SHA、两个 Digest 和结果；不将原失败运行改记成功，也不将镜像存在当作部署完成。跳转策略参照 [OWASP SSRF 防护建议](https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html)，仅声明本文列出的已测试边界，不声称完成全面合规审计。
