[CmdletBinding()]
param()
. (Join-Path $PSScriptRoot 'windows-common.ps1')
$dir=Join-Path $script:Phase2Local ('backups/'+(Get-Date -Format 'yyyyMMdd-HHmmss')+'-'+[guid]::NewGuid().ToString('N').Substring(0,8))
New-Item -ItemType Directory $dir | Out-Null
# Stop all writers/viewers; never copy a live SQLite DB. Source volume remains intact.
$fingerprint=Get-Phase2Fingerprint
Invoke-Phase2Compose -Arguments @('stop')
try {
    Invoke-Phase2Compose -Arguments @('cp','new-api:/data',(Join-Path $dir 'data'))
    $summary=Invoke-Phase2DbCheck -DataDirectory (Join-Path $dir 'data')
    [IO.File]::WriteAllText((Join-Path $dir 'database-summary.json'),($summary -join "`n"),(New-Object Text.UTF8Encoding($false)))
    [IO.File]::WriteAllText((Join-Path $dir 'fingerprint.json'),($fingerprint -join "`n"),(New-Object Text.UTF8Encoding($false)))
    Copy-Item $script:Phase2Env (Join-Path $dir '.env.local')
    Copy-Item $script:Phase2Compose (Join-Path $dir 'compose.yaml')
    # Preserve exact helper/config versions for offline recovery review.
    Copy-Item (Join-Path $PSScriptRoot '*.ps1') $dir
    $fixtureDir=Join-Path $dir 'phase2'
    New-Item -ItemType Directory $fixtureDir | Out-Null
    foreach ($name in @('mock.py','viewer.py','verify.py','db_check.py')) { Copy-Item (Join-Path $PSScriptRoot ('../phase2/'+$name)) $fixtureDir }
    Get-ChildItem $dir -File -Recurse -Force | ForEach-Object {
        @{name=$_.FullName.Substring($dir.Length+1);sha256=(Get-FileHash $_.FullName -Algorithm SHA256).Hash}
    } | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $dir 'sha256.json') -Encoding UTF8
    Test-Phase2Backup -Directory $dir
} finally {
    Invoke-Phase2Compose -Arguments @('up','-d','--wait')
}
Write-Host "Complete PRIVATE backup: $dir. Contains recoverable secrets. Restrict ACLs and use encrypted/offline storage."
