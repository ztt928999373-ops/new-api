$ErrorActionPreference = 'Stop'
Push-Location (Join-Path $PSScriptRoot '../..')
try {
    $sha = (git rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0 -or $sha -notmatch '^[a-f0-9]{40}$') { throw '无法确定源码 SHA' }
    if (git status --porcelain) { throw '请先提交改动；禁止以干净 SHA 标记未提交源码' }
    $env:SUANJIE_SHA = $sha
    docker build --label "org.opencontainers.image.revision=$sha" --label 'org.opencontainers.image.source=https://github.com/ztt928999373-ops/new-api' --label 'org.opencontainers.image.licenses=AGPL-3.0-only' -t "suanjie-api:$sha" .
    if ($LASTEXITCODE -ne 0) { throw '自有源码镜像构建失败' }
    docker compose -f deploy/d1/compose.yml build
    if ($LASTEXITCODE -ne 0) { throw '隔离测试镜像构建失败' }
    Write-Output "已构建本地测试镜像 suanjie-api-test:$sha（未推送）"
} finally { Pop-Location }
