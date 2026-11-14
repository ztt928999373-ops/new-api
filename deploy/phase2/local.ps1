param([ValidateSet('Check','Init','Start','Test','Stop','Backup','RestoreTest')][string]$Action='Check',[string]$BackupPath)
$ErrorActionPreference='Stop'
Set-Location $PSScriptRoot
function Dc { & docker compose --env-file .env.local @args; if ($LASTEXITCODE -ne 0) { throw 'Docker Compose failed' } }
function Check {
  if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw 'Install Docker Desktop with WSL2 and Linux containers first' }
  docker version; if ($LASTEXITCODE -ne 0) { throw 'Start Docker Desktop' }
  docker compose version; if ($LASTEXITCODE -ne 0) { throw 'Compose v2 required' }
  $os = docker info --format '{{.OSType}}'; if ($os -ne 'linux') { throw 'Switch Docker Desktop to Linux containers' }
  if (Get-NetTCPConnection -LocalPort 3067 -State Listen -ErrorAction SilentlyContinue) { Write-Warning 'Port 3067 occupied; check existing phase2 stack or set NEW_API_PORT in .env.local' }
  Get-PSDrive -PSProvider FileSystem | Select-Object Name,Free
  Write-Host 'Check Docker Desktop disk allocation: reserve at least 15 GB. Windows/WSL2 virtualization must be enabled.'
}
function HashManifest($dir) {
  Get-ChildItem $dir -File | Where-Object Name -ne 'sha256.json' | ForEach-Object { @{name=$_.Name;sha256=(Get-FileHash $_.FullName -Algorithm SHA256).Hash} } | ConvertTo-Json | Set-Content (Join-Path $dir 'sha256.json') -Encoding UTF8
}
switch ($Action) {
  Check { Check }
  Init {
    Check
    if (Test-Path .env.local) { throw '.env.local already exists; not overwritten' }
    $rng=[Security.Cryptography.RandomNumberGenerator]::Create()
    $buf=New-Object byte[] 32; $rng.GetBytes($buf); $session=([BitConverter]::ToString($buf)).Replace('-','')
    $rng.GetBytes($buf); $password=([BitConverter]::ToString($buf)).Replace('-','')
    @("SESSION_SECRET=$session","TEST_ADMIN_PASSWORD=$password",'NEW_API_PORT=3067') | Set-Content .env.local -Encoding ASCII
    Write-Host 'Local secrets initialized. Keep .env.local private; use TEST_ADMIN_PASSWORD for phase2root after Test.'
  }
  Start { Check; Dc pull; Dc up -d --wait; Write-Host 'Open http://127.0.0.1:3067; run Test on this dedicated empty environment only.' }
  Test {
    New-Item -ItemType Directory -Force results | Out-Null
    Dc exec -T mock python /phase2/verify.py | Tee-Object results/protocols.jsonl
  }
  Stop { Dc stop }
  Backup {
    $dir=Join-Path $PSScriptRoot ('backups/'+(Get-Date -Format 'yyyyMMdd-HHmmss'))
    New-Item -ItemType Directory -Force $dir | Out-Null
    # Stop the only DB writer before copying SQLite, including WAL/SHM and configuration.
    Dc stop new-api
    try {
      Dc cp new-api:/data (Join-Path $dir 'data')
      $dataPath=Join-Path $dir 'data'
      & docker run --rm --network none --read-only --mount "type=bind,source=$dataPath,target=/backup,readonly" python:3.12.12-slim-bookworm@sha256:593bd06efe90efa80dc4eee3948be7c0fde4134606dd40d8dd8dbcade98e669c python -c "import sqlite3,pathlib; p=pathlib.Path('/backup/one-api.db'); assert p.is_file(), 'SQLite file missing'; c=sqlite3.connect('file:'+str(p)+'?mode=ro',uri=True); r=c.execute('pragma integrity_check').fetchall(); assert r==[('ok',)],r; print('SQLite integrity_check: ok')"
      if ($LASTEXITCODE -ne 0) { throw 'SQLite backup integrity check failed' }
      Copy-Item .env.local,compose.yaml $dir
      Get-ChildItem (Join-Path $dir 'data') -File -Recurse | ForEach-Object { @{name=$_.FullName.Substring($dir.Length+1);sha256=(Get-FileHash $_.FullName -Algorithm SHA256).Hash} } | ConvertTo-Json | Set-Content (Join-Path $dir 'data-sha256.json') -Encoding UTF8
      HashManifest $dir
    } finally { Dc start new-api }
    Write-Host "Private backup: $dir. Contains recoverable API keys and passwords/configuration; restrict Windows ACLs and encrypt offline storage."
  }
  RestoreTest {
    if (-not $BackupPath) { throw 'Supply -BackupPath to an existing backup' }
    $dir=(Resolve-Path $BackupPath).Path
    foreach ($entry in @(Get-Content (Join-Path $dir 'sha256.json') -Raw | ConvertFrom-Json)) { if ((Get-FileHash (Join-Path $dir $entry.name)).Hash -ne $entry.sha256) { throw 'Backup checksum mismatch' } }
    foreach ($entry in @(Get-Content (Join-Path $dir 'data-sha256.json') -Raw | ConvertFrom-Json)) { if ((Get-FileHash (Join-Path $dir $entry.name)).Hash -ne $entry.sha256) { throw 'Database checksum mismatch' } }
    # A distinct Compose project creates distinct network and volume. Never down -v the source project.
    $restore='new-api-phase2-restore-'+(Get-Date -Format 'yyyyMMddHHmmss')
    $prior=$env:NEW_API_PORT; $env:NEW_API_PORT='3068'
    try {
      & docker compose --env-file (Join-Path $dir '.env.local') -p $restore create
      if ($LASTEXITCODE -ne 0) { throw 'Restore create failed' }
      & docker compose --env-file (Join-Path $dir '.env.local') -p $restore cp (Join-Path $dir 'data/.') new-api:/data
      if ($LASTEXITCODE -ne 0) { throw 'Restore copy failed' }
      & docker compose --env-file (Join-Path $dir '.env.local') -p $restore up -d --wait
      if ($LASTEXITCODE -ne 0) { throw 'Restore start failed' }
      & docker compose --env-file (Join-Path $dir '.env.local') -p $restore exec -T mock python /phase2/verify.py --inspect
      if ($LASTEXITCODE -ne 0) { throw 'Restored ledger validation failed' }
      & docker compose --env-file (Join-Path $dir '.env.local') -p $restore exec -T mock python /phase2/verify.py --restore-probe
      if ($LASTEXITCODE -ne 0) { throw 'Restored API probe failed' }
      Write-Host "Restore test project preserved: $restore at http://127.0.0.1:3068. Compare --inspect fingerprints with source BEFORE restore-probe."
    } finally { $env:NEW_API_PORT=$prior }
  }
}
