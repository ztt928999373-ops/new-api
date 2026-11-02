$ErrorActionPreference = 'Stop'
$script:Phase2Compose = Join-Path $PSScriptRoot 'windows-compose.yaml'
$script:Phase2Local = Join-Path $PSScriptRoot '.windows-phase2'
$script:Phase2Env = Join-Path $script:Phase2Local '.env.local'
$script:Phase2Project = 'new-api-win-phase2-rc47'
$script:Phase2Python = 'python:3.12.12-slim-bookworm@sha256:593bd06efe90efa80dc4eee3948be7c0fde4134606dd40d8dd8dbcade98e669c'
function Invoke-Phase2Compose {
    param([Parameter(Mandatory)][string[]]$Arguments, [string]$Project=$script:Phase2Project, [string]$EnvFile=$script:Phase2Env)
    & docker compose --project-directory $PSScriptRoot -f $script:Phase2Compose --env-file $EnvFile -p $Project @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Compose failed: $($Arguments[0])" }
}
function Get-Phase2Fingerprint {
    param([string]$Project=$script:Phase2Project,[string]$EnvFile=$script:Phase2Env)
    Invoke-Phase2Compose -Project $Project -EnvFile $EnvFile -Arguments @('exec','-T','mock','python','/phase2/verify.py','--inspect')
}
function Test-Phase2Backup {
    param([Parameter(Mandatory)][string]$Directory)
    $manifest=Get-Content (Join-Path $Directory 'sha256.json') -Raw | ConvertFrom-Json
    foreach ($entry in @($manifest)) {
        $path=[IO.Path]::GetFullPath((Join-Path $Directory $entry.name))
        $prefix=[IO.Path]::GetFullPath($Directory).TrimEnd([char[]]'\/')+[IO.Path]::DirectorySeparatorChar
        if (-not $path.StartsWith($prefix,[StringComparison]::OrdinalIgnoreCase)) { throw 'Invalid backup manifest path' }
        if ((Get-FileHash $path -Algorithm SHA256).Hash -ne $entry.sha256) { throw "Backup checksum mismatch: $($entry.name)" }
    }
    foreach ($required in @('.env.local',(Join-Path 'data' 'one-api.db'),'fingerprint.json','compose.yaml','database-summary.json')) {
        if (-not (Test-Path (Join-Path $Directory $required))) { throw "Required backup file missing: $required" }
        if (-not (@($manifest | Where-Object name -eq $required).Count)) { throw "Required file not covered by manifest: $required" }
    }
}
function Invoke-Phase2DbCheck {
    param([Parameter(Mandatory)][string]$DataDirectory)
    $checker=(Resolve-Path (Join-Path $PSScriptRoot '../phase2/db_check.py')).Path
    $helper='new-api-win-dbcheck-'+[guid]::NewGuid().ToString('N').Substring(0,12)
    & docker run --name $helper --network none --read-only --mount "type=bind,source=$DataDirectory,target=/backup,readonly" --mount "type=bind,source=$checker,target=/check.py,readonly" $script:Phase2Python python /check.py /backup/one-api.db
    if ($LASTEXITCODE -ne 0) { throw 'SQLite integrity/fingerprint check failed' }
}
