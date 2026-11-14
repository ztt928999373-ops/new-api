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

## 已发布镜像的手动隔离容器验收（不发布）

`v1-d2-image-acceptance.yml` 是独立的 `workflow_dispatch` 工作流；无 push/PR/定时触发，无输入可替换镜像，仅 GitHub 托管 Ubuntu 24.04，权限仅 `contents:read`。仓库、事件和 `refs/heads/product/v1` 同时满足才启动 Job；检出事件完整 SHA，并用只读 Git ref 查询确认仍是当前主线。不会运行 PR checkout。不要把离线测试或其他 ref 的 skipped Job 记为容器通过。

人工合并本工作流后，在 Actions 选择“D2 已发布 Digest 隔离容器验收（仅手动）”，分支选 `product/v1`，核实主线 SHA 后手动运行一次。本次开发不触发它，也不重跑 GHCR publish。实际容器流程由 `.github/automation/d2_container_acceptance.py` 执行，先运行同目录离线安全回归；静态/Mock 通过不代替后续真实流程。

- 固定拉取 `ghcr.io/ztt928999373-ops/suanjie-api@sha256:725d45a3fe8deb3155950a0935bc9873bf0439ef99a425be2dd5f13cbaa21ddf`。全新私有 Docker client 配置不读取原 registry 登录，仅匿名 pull；不足权限即停止。没有登录、build、push、tag、镜像删除或 packages 权限。
- Docker 数据目录所在文件系统拉取前、拉取后及测试末均须至少有 5 GiB 可用空间；不会移动 daemon、改变驱动或执行 prune。该值是余量门禁，不是镜像解包大小估算；Summary 记录实际可用字节和拉取后 image Size。
- 确认 RepoDigest、Config Id、linux/amd64；复用现有 `validateMetadata` 检查原源码 `cd8937c602640fc31c0144994a73266c3d792782`、OCI 身份和入口。三个许可证文件与原发布源码对应的固定 SHA256 比对；不把工作流新提交误当镜像源码。
- 复用 D1 的三个关闭选项和隔离思想，不构建 D1 派生镜像。宿主 Python 在全新私有目录创建 SQLite：RegisterEnabled=false、PasswordRegisterEnabled=false、payment_setting.compliance_confirmed=false。创建全新卷和容器前拒绝同名资源；只将该合成数据复制到尚未启动的新容器；不接触既有数据。
- 两个随机临时密钥只经进程环境注入，不记录值、不提供可用默认值。容器仅 `network=none`、无宿主端口、cap_drop ALL、no-new-privileges，根文件系统只读，仅新卷和临时 /tmp 可写。无 Redis/渠道/真实服务配置，日志限制为 10 MiB × 3。
- 从容器 loopback 检查 Docker health 和 `/api/status`；验证合成注册请求被拒绝、渠道为零、三个关闭选项不变。先停机复制完整 /data 后检查 SQLite `integrity_check`，再启动同一卷验证标记持久化，最后再次停机检查数据库。不是生产管理员或支付验收。
- finally 和 always 兜底只清理本次状态文件中的精确容器/卷名，并要求随机归属标签一致；不扫描删除其他资源，不删除镜像、不清理全局数据。没有创建 Docker 网络，不删除内置 none 网络。缺失资源可幂等重试，权限/daemon 错误不能当作资源不存在。

详细的固定身份、磁盘字节、每项 success/failure/not_run 和安全错误码留在 Actions Job Summary 的 JSON 中；不上传 SQLite、密钥、Docker config、inspect 全文或原始应用日志。测试异常会失败退出，后续未运行项保持 not_run；清理失败同样不能算成功。Runner 硬终止时报告/清理可能来不及完成，须按取消或未确认处理，不认定通过。

合并前离线验证命令：

```sh
python3 -m unittest discover -s .github/automation -p 'test_d2_container_acceptance.py' -v
python3 -m py_compile .github/automation/d2_container_acceptance.py .github/automation/test_d2_container_acceptance.py
actionlint -shellcheck= -pyflakes= .github/workflows/v1-d2-image-acceptance.yml
```

若原镜像缺少健康工具、只读根不兼容、启动退出、容量不足或网络/数据库断言失败，保留真实失败阶段，不改用重新构建的镜像掩盖问题；先报告再由用户决定是否需要修复。所有动态项目真正通过后才能关闭 D2。后续优先做固定 Digest 的隔离部署与恢复验收，不扩展发布基础设施、不进入 C2 或公网运营。

### SQLite 启动失败的证据采集与边界

运行 [43566672555](https://github.com/ztt928999373-ops/new-api/actions/runs/43566672555) 已定位到 SQLite 打开失败（14 / SQLITE_CANTOPEN），尚未确定底层文件系统原因。`glebarez/go-sqlite v1.21.2` 在连接尚未赋值时读取 `sqlite3_errmsg`，会附带空句柄的 “out of memory” 文本；它不是发生容器 OOM 的证据。此前修复只让退出状态和日志可见，没有修复产品或数据权限。以下新增能力尚未在真实固定镜像上运行，离线测试不能替代实测。

| 数据 | 来源与含义 |
|---|---|
| seed 大小、UID/GID、模式 | 复制前的宿主 `lstat`，仅证明源文件，不代表复制后的权限 |
| Image/Container Config.User、WorkingDir | Docker inspect 实际配置，不能当作进程 UID/GID 实测 |
| UsernsMode、CapDrop/CapAdd、SecurityOpt、Privileged、GroupAdd、Mounts.RW | Docker inspect 白名单；不输出 Env、挂载宿主 Source 或 inspect 全文 |
| 原进程有效 UID/GID、用户映射 | 启动后尽力读取宿主 `/proc/PID/{status,uid_map,gid_map,cgroup}`；前后 inspect 的 ID/PID/Running 必须一致，cgroup 必须含完整容器 ID，再转换命名空间 ID。进程已退出、不同宿主/命名空间、读权限不足或竞争均记 UNKNOWN |
| 卷内目录/文件 UID/GID、模式、类型、大小 | 同固定镜像辅助进程的 GNU stat 实测；不读数据库内容。目录不可遍历时，文件元数据可能仍是 UNKNOWN |
| 目录遍历、文件读写权限 | 辅助进程的内核 access 检查；不是原进程的实际文件打开，更不是 SQLite 成功 |
| 目录创建能力 | 辅助进程用 umask 077 创建并删除随机零字节文件；不覆盖数据库、journal、WAL、SHM 或 seed 标记 |
| 创建失败类别 | 对 LC_ALL=C 下 mktemp 的有限错误文本分类（EACCES/EROFS/ENOSPC/ENOENT）；不是 SQLite 的实际 syscall errno。不输出原始工具 stderr |

辅助进程只在诊断 finally 中运行，使用已经存在的同一固定 Digest（pull=never），匹配实际容器 User、UsernsMode、挂载 RW，并保留 network=none、无端口、只读根、cap-drop ALL、no-new-privileges 和资源上限。配置不满足既有隔离契约则停止辅助探针并记 UNKNOWN，不增加权限。只允许本次精确卷名、local 驱动、无驱动选项且归属标签一致的卷。

不能假定镜像内有 shell 或工具。先通过既有 Unix Docker socket 的 archive **HEAD** API 仅查 `/bin/sh`、id/stat/mktemp/rm 的文件元数据，限制符号链接解析路径、次数和15秒超时，不读取任何文件响应体；不支持 API、工具缺失或类型不符均记 UNKNOWN，不启动探针。此步骤只证明可执行文件元数据存在；真正执行失败、stat 格式不支持或协议字段缺失仍记 UNKNOWN。辅助进程不需要 Python、sqlite3 或额外镜像。

辅助输出复用尾部200行、合计64KiB、15秒命令超时的脱敏日志采集，再按固定协议只保留数字、模式、文件类型和枚举；未知 stdout/stderr 不进入报告，截断不能冒充成功。`measurement.access_status=PASS` 仅表示辅助访问检查通过；顶层 storage.status 仍保持 UNKNOWN（或记录直接观测到的失败），不推导原进程身份、数据库写打开、SQLite errno 或 WAL/SHM 创建成功。属主不匹配本身也不是拒绝访问的证明，必须结合实测访问结果。

探针名及归属先原子记录到本轮私有状态文件，再创建容器。最终清理先处理该探针，再处理原容器和卷；仅移除精确名称且标签匹配的本次资源。探针清理失败仍继续尝试原资源清理，最后报告失败；异常不覆盖原验收错误，不执行全局 prune。Runner 被硬终止时仍可能没有完整证据。

下一次需先人工审核、合并并单独批准手动验收。届时优先对比复制前大小与卷内实际文件、原进程身份与辅助身份、挂载 RW、目录遍历及创建失败类别：只有数据支持时才判断路径、权限或挂载根因。文件大小变化可能来自正常应用初始化，不能单凭大小不等断言复制损坏。若这些检查通过但 SQLite 仍失败，errno/journal/WAL/SHM 的具体失败步骤仍是 UNKNOWN，须另行授权有针对性的系统调用证据采集，不能盲目 chown、chmod 777、加权限或修改驱动。
