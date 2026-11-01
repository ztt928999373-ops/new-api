[CmdletBinding()]
param([Parameter(Mandatory)][string]$BackupPath,[int]$Port=3078)
. (Join-Path $PSScriptRoot 'windows-common.ps1')
$dir=(Resolve-Path $BackupPath).Path
Test-Phase2Backup -Directory $dir
if ((Get-FileHash $script:Phase2Compose).Hash -ne (Get-FileHash (Join-Path $dir 'compose.yaml')).Hash) { throw 'Compose changed since backup; review exact backed-up configuration before restoration' }
foreach ($name in @('windows-common.ps1','windows-deploy.ps1','windows-backup.ps1','windows-restore.ps1')) {
    if ((Get-FileHash (Join-Path $PSScriptRoot $name)).Hash -ne (Get-FileHash (Join-Path $dir $name)).Hash) { throw "Helper changed since backup: $name; use the reviewed matching checkout" }
}
foreach ($name in @('mock.py','viewer.py','verify.py','db_check.py')) {
    if ((Get-FileHash (Join-Path $PSScriptRoot ('../phase2/'+$name))).Hash -ne (Get-FileHash (Join-Path $dir ('phase2/'+$name))).Hash) { throw "Fixture changed since backup: $name" }
}
if ($Port -lt 1024 -or $Port -gt 65535 -or $Port -eq 3077) { throw 'Choose a distinct restore port in 1024..65535' }
if (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue) { throw 'Restore port occupied' }
$project='new-api-win-restore-'+[guid]::NewGuid().ToString('N').Substring(0,12)
$envFile=Join-Path $dir '.env.local'
$previous=$env:NEW_API_PORT
$env:NEW_API_PORT=[string]$Port
try {
    Invoke-Phase2Compose -Project $project -EnvFile $envFile -Arguments @('create')
    Invoke-Phase2Compose -Project $project -EnvFile $envFile -Arguments @('cp',(Join-Path $dir 'data/.'),'new-api:/data')
    # Extract the still-stopped restore DB and compare all row hashes BEFORE application startup.
    $verifyDir=Join-Path $script:Phase2Local ('restore-checks/'+$project)
    New-Item -ItemType Directory -Force $verifyDir | Out-Null
    Invoke-Phase2Compose -Project $project -EnvFile $envFile -Arguments @('cp','new-api:/data',(Join-Path $verifyDir 'data'))
    $summary=Invoke-Phase2DbCheck -DataDirectory (Join-Path $verifyDir 'data')
    if (($summary -join "`n").Trim() -ne (Get-Content (Join-Path $dir 'database-summary.json') -Raw).Trim()) { throw 'Restored database row fingerprints differ' }
    Invoke-Phase2Compose -Project $project -EnvFile $envFile -Arguments @('up','-d','--wait')
    $actual=Get-Phase2Fingerprint -Project $project -EnvFile $envFile
    $before=Get-Content (Join-Path $dir 'fingerprint.json') -Raw | ConvertFrom-Json
    $after=($actual -join "`n") | ConvertFrom-Json
    if ($before.fingerprint -ne $after.fingerprint) { throw 'Restored API ledger fingerprint differs' }
    Invoke-Phase2Compose -Project $project -EnvFile $envFile -Arguments @('exec','-T','mock','python','/phase2/verify.py','--restore-probe')
    Write-Host "Independent restored project $project preserved at http://127.0.0.1:$Port. Source project and volume were not overwritten."
} catch {
    $restoreError=$_
    # Roll back availability of the NEW restore project only. Keep all evidence/data.
    try { Invoke-Phase2Compose -Project $project -EnvFile $envFile -Arguments @('stop') }
    catch { Write-Warning "Could not stop failed restore project $project; inspect this project manually." }
    Write-Warning "Restore failed. Project $project and its independent volume are preserved for inspection. Source project/database were never overwritten."
    throw $restoreError
} finally { $env:NEW_API_PORT=$previous }
