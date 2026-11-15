# D3：固定 Digest 预生产隔离部署与恢复准备

当前交付是**配置和操作准备**，不是部署成功或 D3 结项。仅支持专用 Linux amd64、rootful 本机 Docker Engine、Compose v2、Python 3.12+ 的全新合成 SQLite 环境。没有公网或宿主入口，没有真实用户、管理员默认密码、供应商凭据、支付配置或付费调用。不要使用根目录官方 Compose。

## GitHub Actions 一次性 Runner 入口（尚未真实执行）

主线基准 `92659dabad6c918517778d8d7eae915885f4c19a` 已合并 PR #40。本次只开发手动工作流，不触发它；代码合并也不代表批准 full 动态演练。后文保留持久主机的手工操作参考，**Actions 只调用本目录 `acceptance.py`，不解析或复制执行 Markdown 代码块**。

新增工作流 `.github/workflows/v1-d3-isolated-acceptance.yml` 只有 `workflow_dispatch`，不响应 push、PR、定时或其他工作流。固定 `ubuntu-24.04`、Linux x64、受信任仓库和 `product/v1`，仅项目所有者可发起/重新运行。权限仅 `contents: read`，不引用仓库 Secrets、PAT、packages、OIDC 或发布能力；固定审核过的 checkout/upload-artifact 完整 SHA。不要使用 ubuntu-latest、ubuntu-slim、ARM 或付费 Larger Runner。

### 人工运行方法与审批边界

1. 所有者先审阅 Draft PR，决定是否合并；等待**合并后完整 SHA** 的必需 CI 成功。真实演练仍需所有者另外明确批准。
2. 在 Actions 选择“D3 固定 Digest 隔离恢复验收（仅手动）”，分支只能为 `product/v1`；`expected_sha` 填写实时主线完整 SHA，不沿用本节历史基准或 PR head。
3. 默认 `mode=preflight`，`confirmation` 留空。它只检查源码、宿主和 Docker daemon 元数据，不拉镜像、不创建容器/卷、不生成应用密钥；成功结果为 `preflight_pass`，不是 `d3_pass`。
4. 只有另获批准后才选择 `mode=full`，并填写 `confirmation=D3-SYNTHETIC-ONLY`。这会消耗 Runner/网络/临时磁盘资源；任何缺失或错误确认都会在创建资源前拒绝。本轮开发不执行此步骤。
5. 查看 Job Summary、Job 最终状态及 Artifact。Artifact 上传失败、Job 被取消或任何必需阶段非 success，均不得认定验收成功。不要通过手动改 JSON 或重新构建镜像修复失败证据。

触发事件 SHA、工作区 HEAD、输入 SHA 和实时主线必须一致；创建探针、seed 和启动应用前还会重查主线。拒绝 PR ref、其他分支、fork、自托管 Runner、其他操作者及过期 SHA。主线移动会阻断后续运行，但不会阻断该任务自身的归属清理。

### 环境硬门禁与预计资源

- 受控 root 子进程通过 `sudo -n env -i` 运行，只传明确允许的运行上下文；不继承 GITHUB_TOKEN、Actions 运行时凭据或其他宿主秘密。随机应用密钥在 root 子进程内生成，不经命令行或 GITHUB_ENV 输出。
- 本地 `/var/run/docker.sock` 必须为 root 所有的 socket；Docker 必须非 rootless，SecurityOptions 必须可读取。要求 cgroups v2、systemd/cgroupfs 驱动、memory/cpu/pids 控制器及 Docker 对应限额支持标记，Compose v2。未知或缺失立即退出，不安装新 daemon 或降级。
- 现场读取实际 DockerRootDir 及工作目录所在文件系统的余量。拉取前要求至少 8 GiB（5 GiB 保留量加 3 GiB 保守预算），拉取后至少 7 GiB，备份和回滚后至少 5 GiB；这是合成演练的容量门禁，不是对镜像展开大小的精确预测。不移动 Docker 数据目录或删除预装工具来腾空间。
- full 模式先匿名拉取既有固定 Digest，核对 RepoDigest、Config Id、原源码 revision、架构和入口，再运行同一镜像的非业务资源探针。直接读取探针内 memory.max、cpu.max、pids.max；请求参数不等于生效证明，实际值不匹配即退出，尚不启动应用。应用启动后再次核对实际 cgroup 限额。
- 应用最多两个、数据卷两个，探针串行且应用停止时才运行，每次最多一个运行容器。应用 1 GiB / 2 CPU / 256 PID / 128 MiB tmpfs；卷内探针 128 MiB / 0.25 CPU / 32 PID；前置资源探针按应用限额验证。所有容器网络 none、无宿主端口、只读根、cap-drop ALL、no-new-privileges，无真实用户、渠道或支付参数。
- 全新数据、完整备份及回读快照只位于本次 RUNNER_TEMP 的 root 私有目录；应用 env 文件与数据分别置于 private/keys 和数据子目录，0700/0600，不进入 checkout 或 Artifact。合成数据总量限制 256 MiB，快照拒绝符号链接、硬链接、特殊文件及 supplier ring；出现实际凭据场景须停止，不能直接复制旧 ring 恢复。

### 阶段、证据与失败处理

13 个独立阶段为 source、environment、image、resource_limits、seed、startup、backup、restore、restored_startup、persistence、rollback、diagnostics、cleanup。此列表属于 D3，不是重跑 D2 的 13 阶段。

| 状态 | 含义 |
| --- | --- |
| success | 本阶段操作及断言完成 |
| failure | 操作或硬门禁失败，后续业务阶段保持 not_run |
| skipped | preflight 明确不请求完整演练阶段，不能视为 D3 通过 |
| cancelled | 捕获到取消信号或工作流已取消，不能视为通过 |
| not_run | 尚未执行或被前序失败阻断 |
| unknown | 阶段已开始但尚无完成证据；硬终止可能保留此状态 |

执行器只复用 D2 的 seed、数据库校验、有界日志采集及脱敏方法，不调用 D2 main、发布流程或 D2 构造器。seed 与全新恢复卷都先通过同身份卷内 UID/GID=0:0、0600、普通文件、O_RDWR 打开、目录随机文件创建/删除验证，再复制回私有目录验证完整清单/摘要与 SQLite；恢复阶段不只检查宿主解包目录。停机备份保存完整 /data，manifest 配对镜像、实际执行 SHA、Compose、备份和原环境文件摘要。原卷保持停止，恢复到新卷后验完整性、健康及重启 proof；最后回读原卷确认未变，使用原镜像和密钥完成配对回滚，最终停机。

资源精确名称和归属在创建前原子写入私有状态文件。执行器 finally 与工作流 always 两层兜底，只处理本轮名称且匹配 Compose project/自有卷标签/固定镜像/挂载的资源；daemon 错误不是“不存在”，归属不符不删除，其他资源仍继续清理。清理不使用全局 prune、通配删除、down -v 或删除镜像。清理不能确认则报告 failure；取消/断网/Runner 硬终止仍可能来不及执行兜底，不能据此虚报已清理。

**临时 Runner 与后文持久主机的留存政策不同**：原卷仅在本 Job 完成恢复/回滚前保留；诊断后清理本次临时容器、卷、备份和密钥，Runner 回收不提供长期保管。失败也只导出脱敏证据，不能指望登录下一台 Runner 找回数据库。这里只证明同一临时环境的合成恢复流程，不证明异地灾备、长期备份保存或跨版本 schema 回滚。

Artifact 只包含 `report.json` 和 `SHA256SUMS`，7 天留存，报告上限 2 MiB。报告包含环境白名单、13 阶段状态、验证摘要、源码/镜像/备份 SHA-256 及每个应用最多 200 行、合计 64 KiB 的脱敏日志。秘密、数据库、tar 包、manifest 环境配对材料、Docker config、完整 inspect 和原始敏感日志均不上传；日志采集错误或截断会使诊断阶段失败。GitHub 日志遮罩不能替代 Artifact 脱敏。root 报告仅在审核过的两个文件路径内导出为 Runner 用户可读，不递归放宽 private 权限。上传失败同样阻断最终验收；SHA-256 用于核对传输完整性，不是来源签名。

当前仓库 Public，标准 Runner 计算预计免费；Artifact 仍受账号存储配额及计费政策约束，不调整预算、不启用付费 Runner。预计 full 约 5–15 分钟，Job 超时 30 分钟，均为计划值，尚非实测。owner 发起的其他工作流通知属于现有流程，本工作流不新增通知或 Secrets。

### 本次开发验证与未验证项目

离线命令：`python3 -B -m unittest discover -s deploy/d3 -p test_acceptance.py -v`。测试只使用标准库、临时文件、SQLite fixture 和外部 Docker/OS 边界 Mock；涵盖默认预检不建资源、权限/空间/限额拒绝、配对损坏、复制损坏、状态传播、脱敏及归属清理，包括完整编排的离线模拟。不能把模拟中的 d3_pass 当成真实容器通过。

本次开发已完成 17 项离线单元测试（含表驱动子用例），全部通过；actionlint 1.7.7 检查此新增工作流通过（未启用额外 shellcheck/pyflakes），另以 YAML 解析、Bash 语法和内嵌 Python 编译核对手动触发、contents-only、默认预检及固定 actions SHA。工具通过官方 SHA-256 校验后临时使用，不引入项目依赖。未执行 Go/Bun 安装、构建或产品全量测试。

本轮没有触发 workflow_dispatch，没有拉镜像、创建 Docker 资源或实际执行启动/备份/恢复/回滚。源码、工作流静态检查及离线回归通过后，仅提交 Draft PR；实际 Runner 的 rootful、限额、空间、匿名拉取、卷内访问及恢复仍须获批准后验证。总体仍为 D2 已完成、D3 自动化待审核及真实演练，C2 不放行。

## 主线与已有证据

2026-11-15 只读查询 `product/v1`：`a301f89f97fe0e4d07b25793255fee76e7315172`（PR #39）。GitHub API 返回代理 403，改为读取 GitHub Actions 页面；以下运行均对应此完整 SHA，显示 completed/success，相关 Job 页面也已读取。没有触发、重跑或修改 Actions/权限。

| 检查 | 精确主线运行 | 结果 |
| --- | --- | --- |
| D2 发布机制专项检查（无推送） | [43710721722](https://github.com/ztt928999373-ops/new-api/actions/runs/43710721722) / test | Success |
| 后端 CI | [43710721664](https://github.com/ztt928999373-ops/new-api/actions/runs/43710721664) / Backend vet, build, and test | Success |
| 前端 CI | 同上 / Frontend typecheck and test | Success |
| 三数据库安全回归 | [43710721634](https://github.com/ztt928999373-ops/new-api/actions/runs/43710721634) / V1 security regression (3 databases) | Success |

这证明该 SHA 的已有工作流结论，不冒充本地重新执行或下载了全部测试日志。页面提示 runner 系统迁移、upload-artifact Node 版本兼容告警，未显示为失败；本批不修改工作流。合并或部署审批前若主线变化，必须重新读取对应 SHA 的检查，失败、取消、缺失、未知均不能放行。

| 可复用部分 | D3 用法与边界 |
| --- | --- |
| [D2 Run #4 归档](../d2/acceptance/run-4/README.md)、[真实运行 43581480984](https://github.com/ztt928999373-ops/new-api/actions/runs/43581480984) | 沿用固定 Digest、13 项真实容器结果；不重跑 D2，不把 diagnostics 的 incomplete/UNKNOWN 改为成功 |
| [D1 Compose/手册](../d1/README.md) | 复用关闭选项、冷备份、全新卷及镜像/数据配对回滚原则；D1 派生镜像需要 build，不能拿来替代 D2 镜像 |
| `.github/automation/d2_container_acceptance.py` 的 `seed`、`database` | 复用合成数据库初始化、完整性/零渠道/关闭选项/持久标记检查；仅导入函数，不运行 main、Acceptance 或 13 阶段流程 |
| [单机 ops 备份恢复](../../docs/local-install-recovery.md)、`internal/localops/` | 已有数据库/ring 分开备份、manifest 和 KEK 轮换；仅适用于 ops 初始化的状态目录，不能直接用于 D2 的普通 `/data/test.sqlite` |
| 根 Compose | 含官方 latest、示例密码和外露端口，不用于 D3，也不修改它 |

真正缺口为固定已发布镜像的独立部署配置，以及将停机数据、环境秘密、备份摘要、全新卷恢复和配对回滚衔接的步骤。本批只新增本目录两个文件，不新增工作流、测试框架、产品代码或数据库迁移。

## 人工批准前必须明确的执行范围

以下命令**均为批准后操作方案，当前未执行**。拟用所有者批准的专用临时 Linux Docker 主机，不默认使用生产主机或已有 D1/D2 卷。最多两个应用容器、两个全新卷及一个短时卷内探针（应用均停止时才运行，最多一个运行容器）；私有目录还容纳 seed、复制回读、备份和恢复快照，环境秘密另存。应用上限 1 GiB 内存、2 CPU、256 PID、128 MiB tmpfs，日志上限 10 MiB × 3；探针上限 128 MiB、0.25 CPU、32 PID，无秘密或日志持久化。

至少保留 5 GiB 磁盘余量，并额外容纳镜像、原卷、完整备份和恢复卷；按实际 `/data` 大小确认，不能将 5 GiB 当成总容量估算。Docker 管理员等同宿主高权限，可 inspect 环境秘密；停机备份有可用性中断，备份可能包含敏感材料。审批须明确主机、Docker context、本地 socket、私有备份位置、容量、保留期和操作者。禁止远程/生产 Docker context、全机 prune、自动删卷、改权限为 777、买服务器或开放公网。

## 1. 建立私有合成状态并固定身份

以下示例要求在批准后的专用主机上以受控 root shell 执行，`set -Eeuo pipefail`、`umask 077`，禁止 `set -x`。先 `cd` 到核实后的仓库根目录，核对主线/CI；保留实际批准的完整 SHA。不得把秘密写到仓库、命令输出或 PR。

```bash
set -Eeuo pipefail
umask 077
test "$(id -u)" = 0
unset DOCKER_CONTEXT DOCKER_HOST DOCKER_TLS DOCKER_TLS_VERIFY DOCKER_CERT_PATH
unset PYTHONOPTIMIZE # 本手册的 Python assert 是拒绝关卡，不能优化掉
export DOCKER_HOST=unix:///var/run/docker.sock
COMPOSE="$PWD/deploy/d3/compose.yml"
ROOT=$(mktemp -d /var/tmp/suanjie-d3.XXXXXXXX)
KEYROOT=$(mktemp -d /var/tmp/suanjie-d3-keys.XXXXXXXX)
export ROOT KEYROOT
export DOCKER_CONFIG="$ROOT/anonymous-docker"
mkdir -m 700 "$DOCKER_CONFIG"
python3 - <<'PY'
import importlib.util, os, pathlib, secrets
spec = importlib.util.spec_from_file_location('d2', '.github/automation/d2_container_acceptance.py')
d2 = importlib.util.module_from_spec(spec); spec.loader.exec_module(d2)
root = pathlib.Path(os.environ['ROOT']); keys = pathlib.Path(os.environ['KEYROOT'])
name = 'suanjie-d3-' + secrets.token_hex(12)
a, b = secrets.token_hex(32), secrets.token_hex(32)
assert a != b
(keys / 'app.env').write_text(f'D3_PROJECT={name}\nD3_DATA_VOLUME={name}-data\nD3_SESSION_SECRET={a}\nD3_CRYPTO_SECRET={b}\n')
(keys / 'app.env').chmod(0o600)
d2.seed(root / 'seed', secrets.token_hex(24))
PY
ENVFILE="$KEYROOT/app.env"
set -a; source "$ENVFILE"; set +a
IMAGE=ghcr.io/ztt928999373-ops/suanjie-api@sha256:725d45a3fe8deb3155950a0935bc9873bf0439ef99a425be2dd5f13cbaa21ddf
# 只捕获需要的字段在内存中比较，不打印环境秘密或完整 inspect。
check_target() {
  D3_EXPECTED_STATE="$1" D3_CID="$CID" D3_ENVFILE="$ENVFILE" python3 - <<'PY'
import json,os,pathlib,re,subprocess
saved=dict(line.split('=',1) for line in pathlib.Path(os.environ['D3_ENVFILE']).read_text().splitlines())
for key in ('D3_SESSION_SECRET','D3_CRYPTO_SECRET'):
 assert re.fullmatch('[0-9a-f]{64}',saved[key]) and os.environ[key]==saved[key]
assert saved['D3_SESSION_SECRET']!=saved['D3_CRYPTO_SECRET']
project=os.environ['D3_PROJECT']; volume=os.environ['D3_DATA_VOLUME']
assert (project,volume) in [(saved['D3_PROJECT'],saved['D3_DATA_VOLUME']),
 (saved['D3_PROJECT']+'-restore',saved['D3_DATA_VOLUME']+'-restore')]
fmt='''{"state":{{json .State.Status}},"image":{{json .Image}},"user":{{json .Config.User}},"project":{{json (index .Config.Labels "com.docker.compose.project")}},"service":{{json (index .Config.Labels "com.docker.compose.service")}},"environment":{{json .Config.Env}},"mount":{{range .Mounts}}{{if eq .Destination "/data"}}{"name":{{json .Name}},"type":{{json .Type}},"rw":{{json .RW}}}{{end}}{{end}},"userns":{{json .HostConfig.UsernsMode}},"network":{{json .HostConfig.NetworkMode}},"readonly":{{json .HostConfig.ReadonlyRootfs}},"ports":{{json .HostConfig.PortBindings}}}'''
def docker(*args):
 return subprocess.run(['docker',*args],check=True,capture_output=True,text=True).stdout
c=json.loads(docker('inspect','--format',fmt,os.environ['D3_CID']))
assert c['state']==os.environ['D3_EXPECTED_STATE'] and c['user']=='0:0'
assert c['project']==project and c['service']=='app'
assert c['image']=='sha256:a0cc42d43e45ee9ccf69bc12fad8e7c150df78867817dfc727e155e7b186f66a'
assert c['mount']==dict(name=volume,type='volume',rw=True)
assert c['userns']=='' and c['network']=='none' and c['readonly'] is True and not c['ports']
actual=dict(x.split('=',1) for x in c['environment'])
assert actual['SESSION_SECRET']==saved['D3_SESSION_SECRET'] and actual['CRYPTO_SECRET']==saved['D3_CRYPTO_SECRET']
assert actual['SQL_DSN']=='local' and actual['SQLITE_PATH']=='/data/test.sqlite'
assert actual['LOG_SQL_DSN']==actual['REDIS_CONN_STRING']==''
labels=json.loads(docker('volume','inspect','--format','{{json .Labels}}',volume))
assert labels['io.suanjie.d3.owner']==project
other=saved['D3_PROJECT']+'-restore' if project==saved['D3_PROJECT'] else saved['D3_PROJECT']
assert not docker('ps','-q','--filter','label=com.docker.compose.project='+other).strip()
PY
}
check_volume() {
  check_target "$1"
  # 与应用相同镜像、0:0、默认 userns、cap-drop 和挂载模式；仅在应用未运行时执行。
  docker run --rm --name "${D3_PROJECT}-volume-check" \
    --label "io.suanjie.d3.owner=$D3_PROJECT" --label io.suanjie.d3.role=volume-probe \
    --pull=never --network none --read-only --user 0:0 \
    --cap-drop ALL --security-opt no-new-privileges:true --log-driver none \
    --memory 128m --cpus 0.25 --pids-limit 32 \
    --mount "type=volume,src=$D3_DATA_VOLUME,dst=/data,volume-nocopy" \
    --entrypoint /bin/sh "$IMAGE" -eu -c '
      export LC_ALL=C
      umask 077
      test "$(id -u):$(id -g)" = 0:0
      test -d /data && test ! -L /data && test -x /data
      test -f /data/test.sqlite && test ! -L /data/test.sqlite
      test "$(stat -c "%u:%g:%a:%F" /data/test.sqlite)" = "0:0:600:regular file"
      test -r /data/test.sqlite && test -w /data/test.sqlite
      exec 3<>/data/test.sqlite
      exec 3>&-
      probe=$(mktemp /data/.d3-access.XXXXXXXX)
      trap '\''rm -f -- "$probe"'\'' EXIT
      test -f "$probe"
      rm -- "$probe"
      trap - EXIT
      echo D3_VOLUME_ACCESS_PASS
    '
}
EXISTING=$(docker ps -aq --filter "label=com.docker.compose.project=$D3_PROJECT")
test -z "$EXISTING"
VOLUMES=$(docker volume ls --format '{{.Name}}')
if printf '%s\n' "$VOLUMES" | grep -qxF "$D3_DATA_VOLUME"; then exit 1; fi
# 只在完整资源列表读取成功后判断不存在；daemon/权限错误直接停止。
docker info --format '{{.OSType}} {{.Architecture}}'
docker pull "$IMAGE" # 仅此固定 Digest，匿名 pull；失败停止，不登录、不扩权或重发布
test "$(docker image inspect --format '{{.Id}}' "$IMAGE")" = sha256:a0cc42d43e45ee9ccf69bc12fad8e7c150df78867817dfc727e155e7b186f66a
test "$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$IMAGE")" = linux/amd64
test "$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$IMAGE")" = cd8937c602640fc31c0144994a73266c3d792782
docker compose --env-file "$ENVFILE" -f "$COMPOSE" config --quiet
docker volume create --label io.suanjie.d3.owner="$D3_PROJECT" "$D3_DATA_VOLUME"
docker compose --env-file "$ENVFILE" -f "$COMPOSE" create --no-build app
CID=$(docker compose --env-file "$ENVFILE" -f "$COMPOSE" ps -aq app)
test -n "$CID"
test "$(docker inspect --format '{{.State.Status}}' "$CID")" = created
docker cp "$ROOT/seed/." "$CID:/data"
check_volume created
mkdir -m 700 "$ROOT/seed-check"
docker cp -a "$CID:/data/." "$ROOT/seed-check"
python3 - <<'PY'
import os,pathlib,sqlite3,stat
r=pathlib.Path(os.environ['ROOT']); p=r/'seed-check/test.sqlite'; s=p.lstat()
assert stat.S_ISREG(s.st_mode) and s.st_uid==0 and s.st_gid==0 and stat.S_IMODE(s.st_mode)==0o600
assert p.read_bytes()==(r/'seed/test.sqlite').read_bytes()
assert (r/'seed-check/d2-proof').read_bytes()==(r/'seed/d2-proof').read_bytes()
with sqlite3.connect(p.as_uri()+'?mode=ro',uri=True) as c:
 assert c.execute('PRAGMA integrity_check').fetchall()==[('ok',)]
 assert dict(c.execute('SELECT key,value FROM options'))==dict.fromkeys(
  ['RegisterEnabled','PasswordRegisterEnabled','payment_setting.compliance_confirmed'],'false')
PY
```

`seed` 使用 root 生成的 0600 数据库，遵守 D2 #38 的属主修复。`check_volume` 在实际命名卷内检查 UID/GID、0600、普通文件/非符号链接、目录遍历、数据库 O_RDWR 打开及随机零字节文件创建/删除；不会向数据库写入内容。此辅助进程证明访问能力，不冒充应用 SQLite 事务成功，后者仍须后续健康和数据库检查。复制回读使用 `docker cp -a` 保留卷内 UID/GID，核对字节、选项与 proof。不执行 D2 验收工作流。任何证据未知即停止，不猜测 chown。Compose 本身不初始化数据库，不能跳过 seed 直接 up，也不能挂载既有卷。

`check_target` 在首次 seed、恢复、启动、停机备份及回滚关卡核对实际容器身份、project、卷名/归属/RW、网络和环境配对。Shell 导出的变量优先于 `--env-file`；恢复时只允许项目名和卷名追加 `-restore`，秘密必须仍与原 0600 env 文件一致，并与容器实际注入值比较。不要重新 source 原 env 文件覆盖恢复项目名，不要使用 Compose `-p`、额外 override 或修改 userns。默认 userns 与同一 daemon 保持一致，若非空则拒绝。外部卷配置不代替这些核对。

探针名称固定为当前唯一项目的 `-volume-check`，带本轮归属标签；同名存在时 Docker 拒绝创建，不删除已有容器。正常结束由 `--rm` 清理自己的探针；硬终止后若仍存在，先核对精确名称及归属再人工停止，不能启动应用与其并发访问卷。

`SESSION_SECRET` / `CRYPTO_SECRET` 在单独私有目录安全保留，原容器重建与配对恢复复用该对秘密，不输出完整 compose config、inspect 或 env。这里只做零渠道普通 SQLite；若出现供应商 ring、渠道、真实数据或需恢复的活动凭据，停止此流程，改用原有 ops 的配对备份与新 KEK 轮换流程，另行审批，不能通过复制旧 ring 后继续加密。

## 2. 健康检查与隔离边界

```bash
check_volume created # 首次启动或恢复首次启动；同一 shell 中复用上文函数
# 从可能启动应用的关卡起，任何错误仅尽力停下精确当前容器，不删除、不自动回滚。
stop_on_error() {
  rc=$?
  trap - ERR
  set +e
  owner=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$CID" 2>/dev/null)
  if [ "$owner" = "$D3_PROJECT" ]; then
    docker stop --time 30 "$CID" >/dev/null 2>&1
    state=$(docker inspect --format '{{.State.Status}}' "$CID" 2>/dev/null)
    [ "$state" = exited ] || echo D3_STOP_UNCONFIRMED >&2
  else
    echo D3_STOP_OWNERSHIP_UNCONFIRMED >&2
  fi
  exit "$rc"
}
trap stop_on_error ERR
docker compose --env-file "$ENVFILE" -f "$COMPOSE" up -d --no-build --no-recreate --pull never --wait --wait-timeout 180 app
check_target running
test "$(docker inspect --format '{{.State.Health.Status}}' "$CID")" = healthy
docker exec "$CID" wget -q -T 5 -O - http://127.0.0.1:3000/api/status \
  | python3 -c 'import json,sys; assert json.load(sys.stdin)["success"] is True'
test "$(docker inspect --format '{{.HostConfig.NetworkMode}}' "$CID")" = none
test "$(docker inspect --format '{{len .HostConfig.PortBindings}}' "$CID")" = 0
```

无 host port、网络 none、只读根、cap-drop ALL 和 no-new-privileges；注册与支付通过 seed 的三个关闭选项禁用，零渠道与无出站阻断真实付费调用。健康不代表管理员登录、支付、安全供应商接入或公网 HTTPS 已验收。不要初始化管理员或填供应商/支付参数，不为演示开启网络。手动修改数据库或选项后必须停止，不能继续沿用本流程的合成数据结论。

## 3. 完整停机备份与配对记录

先停止并确认退出，保持操作者对 Docker 的排他控制，直到复制完成；不能有人并发启动或替换容器。不要复制运行中的 SQLite，不仅复制主数据库，也保留完整 `/data`（含 WAL/SHM 等若存在）。

```bash
check_target running
docker compose --env-file "$ENVFILE" -f "$COMPOSE" stop --timeout 30 app
check_volume exited
test "$(docker inspect --format '{{.State.Status}}' "$CID")" = exited
test "$(docker inspect --format '{{.State.ExitCode}}' "$CID")" = 0
BACKUP=$(mktemp -d "$ROOT/backup.XXXXXXXX")
export BACKUP
mkdir -m 700 "$BACKUP/data"
docker cp -a "$CID:/data/." "$BACKUP/data"
python3 - <<'PY'
import importlib.util, os, pathlib, sqlite3, stat
spec = importlib.util.spec_from_file_location('d2', '.github/automation/d2_container_acceptance.py')
d2 = importlib.util.module_from_spec(spec); spec.loader.exec_module(d2)
root = pathlib.Path(os.environ['ROOT']); data = pathlib.Path(os.environ['BACKUP']) / 'data'
assert all(not p.is_symlink() and (p.is_dir() or (p.is_file() and p.stat().st_nlink == 1)) for p in data.rglob('*'))
assert not list(data.rglob('*ring*')), '发现 ring，停止普通文件复制恢复流程'
d2.database(data, (root / 'seed/d2-proof').read_text())
with sqlite3.connect((data/'test.sqlite').as_uri()+'?mode=ro',uri=True) as c:
 assert c.execute('SELECT count(*) FROM users').fetchone()==(0,)
assert stat.S_IMODE((data / 'test.sqlite').stat().st_mode) == 0o600
PY
tar -C "$BACKUP/data" -cf "$BACKUP/data.tar" .
sha256sum "$BACKUP/data.tar" > "$BACKUP/data.tar.sha256"
python3 - <<'PY'
import datetime, hashlib, json, os, pathlib
b = pathlib.Path(os.environ['BACKUP']); k = pathlib.Path(os.environ['KEYROOT']) / 'app.env'
manifest = dict(format=1, synthetic_only=True,
 image='ghcr.io/ztt928999373-ops/suanjie-api@sha256:725d45a3fe8deb3155950a0935bc9873bf0439ef99a425be2dd5f13cbaa21ddf',
 image_revision='cd8937c602640fc31c0144994a73266c3d792782',
 config_digest='sha256:a0cc42d43e45ee9ccf69bc12fad8e7c150df78867817dfc727e155e7b186f66a',
 preparation_sha='a301f89f97fe0e4d07b25793255fee76e7315172',
 source_project=os.environ['D3_PROJECT'], source_volume=os.environ['D3_DATA_VOLUME'],
 created_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),
 data_sha256=hashlib.sha256((b/'data.tar').read_bytes()).hexdigest(),
 environment_sha256=hashlib.sha256(k.read_bytes()).hexdigest())
with (b/'manifest.json').open('x') as f: json.dump(manifest,f,indent=2)
PY
```

manifest 最后成功写入才形成可用备份；中断产生的目录保留但不可恢复。校验和检测损坏/配对错误，不是防恶意替换的签名；保护 manifest、原 env 文件和备份目录，分开安全保存，按所有者批准的方式加密保管。本批不提供或调用外部上传、KMS 或付费存储。记录实际磁盘余量和各步退出状态，不收集密钥、原始 SQL/日志或 inspect 全文到 PR。

## 4. 全新卷恢复与完整性校验

原容器保持停止、原卷不改；先验证归档及环境配对，再解包到全新目录。不得仅以“新容器启动”代替恢复检查。

```bash
python3 - <<'PY'
import hashlib,json,os,pathlib,tarfile
b=pathlib.Path(os.environ['BACKUP']); m=json.loads((b/'manifest.json').read_text())
assert m['format']==1 and m['synthetic_only'] is True
assert m['image']=='ghcr.io/ztt928999373-ops/suanjie-api@sha256:725d45a3fe8deb3155950a0935bc9873bf0439ef99a425be2dd5f13cbaa21ddf'
assert m['config_digest']=='sha256:a0cc42d43e45ee9ccf69bc12fad8e7c150df78867817dfc727e155e7b186f66a'
assert m['image_revision']=='cd8937c602640fc31c0144994a73266c3d792782'
assert m['source_project']==os.environ['D3_PROJECT'] and m['source_volume']==os.environ['D3_DATA_VOLUME']
assert hashlib.sha256((b/'data.tar').read_bytes()).hexdigest()==m['data_sha256']
assert hashlib.sha256((pathlib.Path(os.environ['KEYROOT'])/'app.env').read_bytes()).hexdigest()==m['environment_sha256']
target=b/'restore-data'; target.mkdir(mode=0o700,exist_ok=False)
with tarfile.open(b/'data.tar') as t:
 assert all(x.isfile() or x.isdir() for x in t.getmembers())
 t.extractall(target,filter='data')
PY
ORIGINAL_CID="$CID"
ORIGINAL_PROJECT="$D3_PROJECT"
ORIGINAL_VOLUME="$D3_DATA_VOLUME"
export D3_PROJECT="${ORIGINAL_PROJECT}-restore"
export D3_DATA_VOLUME="${ORIGINAL_VOLUME}-restore"
# 必须证明该项目不存在、该卷不存在；daemon/权限错误不是不存在。
EXISTING=$(docker ps -aq --filter "label=com.docker.compose.project=$D3_PROJECT")
test -z "$EXISTING"
VOLUMES=$(docker volume ls --format '{{.Name}}')
if printf '%s\n' "$VOLUMES" | grep -qxF "$D3_DATA_VOLUME"; then exit 1; fi
docker volume create --label io.suanjie.d3.owner="$D3_PROJECT" "$D3_DATA_VOLUME"
docker compose --env-file "$ENVFILE" -f "$COMPOSE" create --no-build app
CID=$(docker compose --env-file "$ENVFILE" -f "$COMPOSE" ps -aq app)
test -n "$CID"
test "$(docker inspect --format '{{.State.Status}}' "$CID")" = created
docker cp "$BACKUP/restore-data/." "$CID:/data"
check_volume created
mkdir -m 700 "$ROOT/restore-check"
docker cp -a "$CID:/data/." "$ROOT/restore-check"
python3 - <<'PY'
import hashlib,importlib.util,os,pathlib,sqlite3,stat
spec=importlib.util.spec_from_file_location('d2','.github/automation/d2_container_acceptance.py')
d2=importlib.util.module_from_spec(spec);spec.loader.exec_module(d2)
data=pathlib.Path(os.environ['ROOT'])/'restore-check'
source=pathlib.Path(os.environ['BACKUP'])/'restore-data'
def files(root):
 result={}
 for p in root.rglob('*'):
  assert not p.is_symlink()
  if p.is_file():
   assert p.stat().st_nlink==1
   result[str(p.relative_to(root))]=hashlib.sha256(p.read_bytes()).hexdigest()
  else: assert p.is_dir()
 return result
assert files(data)==files(source)
d2.database(data,(pathlib.Path(os.environ['ROOT'])/'seed/d2-proof').read_text())
s=(data/'test.sqlite').lstat()
assert stat.S_ISREG(s.st_mode) and s.st_uid==s.st_gid==0 and stat.S_IMODE(s.st_mode)==0o600
with sqlite3.connect((data/'test.sqlite').as_uri()+'?mode=ro',uri=True) as c:
 assert c.execute('SELECT count(*) FROM users').fetchone()==(0,)
PY
```

启动恢复容器前，上述检查针对真实恢复卷执行访问探针，并复制回全新 `restore-check` 校验完整文件清单/摘要、proof、integrity_check、关闭选项、零渠道与权限；不是只检查宿主 restore-data。然后执行第 2 节首次启动/健康检查。恢复后再执行以下停机、重启持久化和数据库验证，原项目始终保持停止：

```bash
check_target running
docker compose --env-file "$ENVFILE" -f "$COMPOSE" stop --timeout 30 app
check_volume exited
test "$(docker inspect --format '{{.State.ExitCode}}' "$CID")" = 0
docker compose --env-file "$ENVFILE" -f "$COMPOSE" up -d --no-build --no-recreate --pull never --wait --wait-timeout 180 app
check_target running
test "$(docker exec "$CID" cat /data/d2-proof)" = "$(cat "$ROOT/seed/d2-proof")"
docker exec "$CID" wget -q -T 5 -O - http://127.0.0.1:3000/api/status \
  | python3 -c 'import json,sys; assert json.load(sys.stdin)["success"] is True'
docker compose --env-file "$ENVFILE" -f "$COMPOSE" stop --timeout 30 app
check_volume exited
test "$(docker inspect --format '{{.State.ExitCode}}' "$CID")" = 0
FINAL_CHECK=$(mktemp -d "$ROOT/restored-final.XXXXXXXX")
export FINAL_CHECK
docker cp -a "$CID:/data/." "$FINAL_CHECK"
python3 - <<'PY'
import importlib.util,os,pathlib,sqlite3
spec=importlib.util.spec_from_file_location('d2','.github/automation/d2_container_acceptance.py')
d2=importlib.util.module_from_spec(spec);spec.loader.exec_module(d2)
data=pathlib.Path(os.environ['FINAL_CHECK'])
d2.database(data,(pathlib.Path(os.environ['ROOT'])/'seed/d2-proof').read_text())
with sqlite3.connect((data/'test.sqlite').as_uri()+'?mode=ro',uri=True) as c:
 assert c.execute('SELECT count(*) FROM users').fetchone()==(0,)
print('D3_RESTORED_DATABASE_PASS')
PY
```

必须记录复制前后文件摘要、卷内访问关卡、恢复启动健康、重启 proof、最终完整性与回滚结果。任一步失败使当前 `set -e` shell 终止，ERR trap 尽力停止归属匹配的精确当前容器，不自动启动原项目。硬终止、daemon 不可用或归属不明可能使停止无法确认；出现 `D3_STOP_UNCONFIRMED` / `D3_STOP_OWNERSHIP_UNCONFIRMED` 时由操作者核实，不能据此认定已停机或开始回滚。保留原卷、失败卷、备份和配对 env；不得继续后续步骤。此处完整流程**未动态验证**，不以 D2 同卷重启成功替代全新卷恢复成功。

## 5. 配对回滚与失败处理

- 当前只有一个已验证 Digest，回滚准备是“该 Digest + 对应停机完整数据 + 原环境秘密 + manifest”的配对；不是声称已验证两个应用版本间的 schema 降级。
- 恢复失败：停止恢复项目，确认所有进程退出，保留失败卷/目录与安全错误信息；不覆盖原卷、不自动删除任何资源。恢复项目停止后才允许切回原项目。原项目是 exited 状态，不能套用第 2 节的 `check_volume created`；按以下配对回滚关卡执行。
- 未来升级：先记录旧 Digest 和旧停机备份；候选镜像必须另经发布与验收、另行批准。候选迁移不能覆盖旧卷。失败时停候选，用旧 Digest 在新卷恢复旧备份和配对秘密；不能只换回旧镜像读取新 schema。
- 默认全部保留。所有者确认备份可恢复及保留期后，才可按精确容器/卷名和 `io.suanjie.d3.owner` 标签人工清理；不用 `down -v`、全机 prune、通配删除或删除镜像。原项目和恢复项目不同时承接请求。

```bash
# 仅在恢复项目已按精确 CID 停止并确认退出后执行。变量丢失时先从私有记录恢复，不猜测名称。
test "$(docker inspect --format '{{.State.Status}}' "$CID")" = exited
export D3_PROJECT="$ORIGINAL_PROJECT" D3_DATA_VOLUME="$ORIGINAL_VOLUME"
CID="$ORIGINAL_CID"
check_volume exited
# 原卷应仍与停机备份逐文件一致；回读到新的 rollback-check，复用上文 files() 摘要比较后才能启动。
mkdir -m 700 "$ROOT/rollback-check"
docker cp -a "$CID:/data/." "$ROOT/rollback-check"
python3 - <<'PY'
import hashlib,os,pathlib
def hashes(root):
 result={}
 for p in root.rglob('*'):
  assert not p.is_symlink() and (p.is_dir() or p.is_file())
  if p.is_file(): result[str(p.relative_to(root))]=hashlib.sha256(p.read_bytes()).hexdigest()
 return result
assert hashes(pathlib.Path(os.environ['ROOT'])/'rollback-check')==hashes(pathlib.Path(os.environ['BACKUP'])/'data')
PY
docker compose --env-file "$ENVFILE" -f "$COMPOSE" up -d --no-build --no-recreate --pull never --wait --wait-timeout 180 app
check_target running
test "$(docker exec "$CID" cat /data/d2-proof)" = "$(cat "$ROOT/seed/d2-proof")"
docker exec "$CID" wget -q -T 5 -O - http://127.0.0.1:3000/api/status \
  | python3 -c 'import json,sys; assert json.load(sys.stdin)["success"] is True'
# 记录配对回滚健康与 proof 结果，演练结束保持停机；不删除原卷或失败卷。
docker compose --env-file "$ENVFILE" -f "$COMPOSE" stop --timeout 30 app
check_target exited
test "$(docker inspect --format '{{.State.ExitCode}}' "$CID")" = 0
```

## 验证状态、产品进度与 C2

本批已执行并通过：Compose v2.40.3 JSON 解析，4 个必需变量分别缺失时均拒绝；固定 Digest/无 build/无端口/网络 none/只读根/权限与资源限制契约；所有手册 Bash 块 `bash -n` 与内嵌 Python 编译（仅语法，不执行部署命令）；复用 seed 在临时目录生成 0600 合成 SQLite、integrity_check=ok、三个关闭选项；在归档目录执行 `sha256sum -c SHA256SUMS`；`git diff --check`。归档摘要首次从仓库根目录调用因相对路径错误失败，改在 SHA256SUMS 所在目录重跑后通过，未修改归档。未安装 Go/Bun 依赖，未重跑全量测试或 D2 动态验收。

尚未验证：D3 真实拉取/创建/启动、复制后权限、健康、停机冷备份、全新卷恢复、恢复后完整性与回滚演练；也未验证此宿主的 rootful Docker 能力、实际容量、私有备份加密/异地保管、长期稳定性。Compose 静态通过不代表这些步骤成功。动态步骤须先获得所有者批准，任何失败停止并报告，不扩权或换镜像绕过。

进度按门禁描述，不虚构整体百分比：D2 镜像发布与 Run #4 已有证据，PR #39 原始归档已合并；D3 为准备阶段，静态结果与动态结果分开；生产可用性、真实管理员业务、备份保管及恢复演练仍需逐项完成。当前具备提交隔离部署方案供批准的基础，**尚不具备宣称 D3 部署/恢复已通过或可公网运营的条件**。

C2 不因本次配置自动放行，也不新增 C2 工作流或供应商接入。此轮保持注册、支付和真实 API 禁用；C2 的具体范围及门禁仍由所有者确认。下一步仅申请一次专用临时环境的 D3 部署/恢复/回滚演练，记录结果后决定是否推进。最终合并、真实部署与阶段结项均由项目所有者决定。

## PR #40 专项静态复核

复核前 HEAD 为 `4e073933fe9e1cbb48f564b72ec181f1f863b672`，主线仍为上文 PR #39 SHA。只读查询该 HEAD 的运行，未手动重跑成功检查：

| 状态 | 检查与证据 |
| --- | --- |
| success | [CI 43712319753](https://github.com/ztt928999373-ops/new-api/actions/runs/43712319753)：后端、前端；[43712319711](https://github.com/ztt928999373-ops/new-api/actions/runs/43712319711)：三数据库安全回归 |
| success | [43712319841](https://github.com/ztt928999373-ops/new-api/actions/runs/43712319841)：技术主管 manager；这是流程检查，不替代人工批准或动态恢复 |
| skipped | F1、F1.1、F2 系列及 Local recovery 共 9 个分支限定工作流，现有 `jobs.if` 不匹配 D3 分支；不能记成通过 |
| not_run | D2 专项及 automation safety：本次两个 D3 路径不匹配其 PR paths 触发条件；D2 真实容器手动工作流也未触发 |
| failure / cancelled / in_progress | 此复核前 HEAD 的已列运行中均未观察到；不将此结论沿用为修复后 HEAD 的结果 |

确认并修复：首次 seed 缺少真实卷内访问验证；恢复检查错误地只看宿主 restore-data；未核对实际容器注入密钥/挂载与 env 配对；恢复后验证和失败回滚只给了概述。现在加入同身份卷内关卡、恢复卷回读/清单比较、配对与互斥检查、恢复重启/完整性证据步骤和失败停机 ERR trap；回滚显式接受 exited 原容器，而不是误用 created 关卡。只修改本目录原有两个文件，不动产品或工作流。

已执行的专项验证：Compose 解析确认原/恢复项目、卷名独立，密钥保持配对；6 个 Bash 块和 9 个内嵌 Python 块语法通过；实际 `check_target` 代码的 13 个 Mock 用例（2 个接受、11 个拒绝）通过，覆盖错身份/卷/密钥/归属/状态/userns/隔离与另一项目仍运行；ERR trap 的归属拒绝和停止路径 Mock 通过；探针实际参数与 `/bin/sh -n` 通过。Mock 没有访问 Docker daemon、创建容器或验证真实卷内文件，不能将它记为真实部署通过。

修复推送会由现有 CI 自然检查新 HEAD；最新状态以交付时查询为准，旧 HEAD 成功不能覆盖新 HEAD 的 failure、cancelled、in_progress 或 not_run。人工合并仅表示接受准备配置/手册；仍须所有者单独批准专用主机、容量和秘密/备份保管方案，才能执行卷内权限、健康、冷备份、全新卷恢复与配对回滚演练。D2 无需返工，D3 保持待动态验证，整体计划只补齐这次演练关卡，不启动 C2。
