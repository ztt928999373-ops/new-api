# D3：固定 Digest 预生产隔离部署与恢复准备

当前交付是**配置和操作准备**，不是部署成功或 D3 结项。仅支持专用 Linux amd64、rootful 本机 Docker Engine、Compose v2、Python 3.12+ 的全新合成 SQLite 环境。没有公网或宿主入口，没有真实用户、管理员默认密码、供应商凭据、支付配置或付费调用。不要使用根目录官方 Compose。

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

以下命令**均为批准后操作方案，当前未执行**。拟用所有者批准的专用临时 Linux Docker 主机，不默认使用生产主机或已有 D1/D2 卷。预计最多两个应用容器（原卷与恢复卷各一个，运行时只允许一个）、两个全新卷、两个私有数据目录和单独的环境秘密目录；每个运行容器上限 1 GiB 内存、2 CPU、256 PID、128 MiB tmpfs，日志上限 10 MiB × 3。

至少保留 5 GiB 磁盘余量，并额外容纳镜像、原卷、完整备份和恢复卷；按实际 `/data` 大小确认，不能将 5 GiB 当成总容量估算。Docker 管理员等同宿主高权限，可 inspect 环境秘密；停机备份有可用性中断，备份可能包含敏感材料。审批须明确主机、Docker context、本地 socket、私有备份位置、容量、保留期和操作者。禁止远程/生产 Docker context、全机 prune、自动删卷、改权限为 777、买服务器或开放公网。

## 1. 建立私有合成状态并固定身份

以下示例要求在批准后的专用主机上以受控 root shell 执行，`set -euo pipefail`、`umask 077`，禁止 `set -x`。先 `cd` 到核实后的仓库根目录，核对主线/CI；保留实际批准的完整 SHA。不得把秘密写到仓库、命令输出或 PR。

```bash
set -euo pipefail
umask 077
test "$(id -u)" = 0
unset DOCKER_CONTEXT DOCKER_HOST DOCKER_TLS DOCKER_TLS_VERIFY DOCKER_CERT_PATH
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

`seed` 使用 root 生成的 0600 数据库，遵守 D2 #38 的属主修复。上述复制回读使用 `docker cp -a` 保留卷内 UID/GID，核对字节、权限、选项与 proof；不执行 D2 验收工作流。任何证据未知即停止，不猜测 chown。Compose 本身不初始化数据库，不能跳过 seed 直接 up，也不能挂载既有卷。外部卷要求只防止隐式创建，不代替操作者核对新卷、空卷和归属。

`SESSION_SECRET` / `CRYPTO_SECRET` 在单独私有目录安全保留，原容器重建与配对恢复复用该对秘密，不输出完整 compose config、inspect 或 env。这里只做零渠道普通 SQLite；若出现供应商 ring、渠道、真实数据或需恢复的活动凭据，停止此流程，改用原有 ops 的配对备份与新 KEK 轮换流程，另行审批，不能通过复制旧 ring 后继续加密。

## 2. 健康检查与隔离边界

```bash
docker compose --env-file "$ENVFILE" -f "$COMPOSE" up -d --no-build --no-recreate --pull never --wait --wait-timeout 180 app
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
docker compose --env-file "$ENVFILE" -f "$COMPOSE" stop --timeout 30 app
test "$(docker inspect --format '{{.State.Status}}' "$CID")" = exited
test "$(docker inspect --format '{{.State.ExitCode}}' "$CID")" = 0
BACKUP=$(mktemp -d "$ROOT/backup.XXXXXXXX")
export BACKUP
mkdir -m 700 "$BACKUP/data"
docker cp "$CID:/data/." "$BACKUP/data"
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
python3 - <<'PY'
import importlib.util,os,pathlib,sqlite3,stat
spec=importlib.util.spec_from_file_location('d2','.github/automation/d2_container_acceptance.py')
d2=importlib.util.module_from_spec(spec);spec.loader.exec_module(d2)
data=pathlib.Path(os.environ['BACKUP'])/'restore-data'
d2.database(data,(pathlib.Path(os.environ['ROOT'])/'seed/d2-proof').read_text())
assert stat.S_IMODE((data/'test.sqlite').stat().st_mode)==0o600
with sqlite3.connect((data/'test.sqlite').as_uri()+'?mode=ro',uri=True) as c:
 assert c.execute('SELECT count(*) FROM users').fetchone()==(0,)
PY
```

在启动恢复容器前，对 `restore-data` 再运行第 3 节的 `d2.database` 校验（路径改为 `restore-data`），核对 proof、integrity_check、关闭选项、零渠道及文件权限。然后执行第 2 节健康/隔离检查；再次停机复制恢复卷到另一个从未存在的目录，再运行相同数据库检查。恢复前后须记录同一 proof、两轮数据库完整性、健康和配置边界。此处完整流程**未动态验证**，不以 D2 的同卷重启成功代替“从备份恢复到全新卷”成功。

## 5. 配对回滚与失败处理

- 当前只有一个已验证 Digest，回滚准备是“该 Digest + 对应停机完整数据 + 原环境秘密 + manifest”的配对；不是声称已验证两个应用版本间的 schema 降级。
- 恢复失败：停止恢复项目，确认所有进程退出，保留失败卷/目录与安全错误信息；不覆盖原卷、不自动删除任何资源。恢复项目停止后才允许切回原项目。重新设置 `D3_PROJECT="$ORIGINAL_PROJECT"`、`D3_DATA_VOLUME="$ORIGINAL_VOLUME"`、`CID="$ORIGINAL_CID"`，核对原卷未被改写和固定镜像身份，按第 2 节启动原实例。
- 未来升级：先记录旧 Digest 和旧停机备份；候选镜像必须另经发布与验收、另行批准。候选迁移不能覆盖旧卷。失败时停候选，用旧 Digest 在新卷恢复旧备份和配对秘密；不能只换回旧镜像读取新 schema。
- 默认全部保留。所有者确认备份可恢复及保留期后，才可按精确容器/卷名和 `io.suanjie.d3.owner` 标签人工清理；不用 `down -v`、全机 prune、通配删除或删除镜像。原项目和恢复项目不同时承接请求。

## 验证状态、产品进度与 C2

本批已执行并通过：Compose v2.40.3 JSON 解析，4 个必需变量分别缺失时均拒绝；固定 Digest/无 build/无端口/网络 none/只读根/权限与资源限制契约；所有手册 Bash 块 `bash -n` 与内嵌 Python 编译（仅语法，不执行部署命令）；复用 seed 在临时目录生成 0600 合成 SQLite、integrity_check=ok、三个关闭选项；在归档目录执行 `sha256sum -c SHA256SUMS`；`git diff --check`。归档摘要首次从仓库根目录调用因相对路径错误失败，改在 SHA256SUMS 所在目录重跑后通过，未修改归档。未安装 Go/Bun 依赖，未重跑全量测试或 D2 动态验收。

尚未验证：D3 真实拉取/创建/启动、复制后权限、健康、停机冷备份、全新卷恢复、恢复后完整性与回滚演练；也未验证此宿主的 rootful Docker 能力、实际容量、私有备份加密/异地保管、长期稳定性。Compose 静态通过不代表这些步骤成功。动态步骤须先获得所有者批准，任何失败停止并报告，不扩权或换镜像绕过。

进度按门禁描述，不虚构整体百分比：D2 镜像发布与 Run #4 已有证据，PR #39 原始归档已合并；D3 为准备阶段，静态结果与动态结果分开；生产可用性、真实管理员业务、备份保管及恢复演练仍需逐项完成。当前具备提交隔离部署方案供批准的基础，**尚不具备宣称 D3 部署/恢复已通过或可公网运营的条件**。

C2 不因本次配置自动放行，也不新增 C2 工作流或供应商接入。此轮保持注册、支付和真实 API 禁用；C2 的具体范围及门禁仍由所有者确认。下一步仅申请一次专用临时环境的 D3 部署/恢复/回滚演练，记录结果后决定是否推进。最终合并、真实部署与阶段结项均由项目所有者决定。
