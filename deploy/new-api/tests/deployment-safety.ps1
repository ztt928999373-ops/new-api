param([Parameter(Mandatory)][string]$Source,[Parameter(Mandatory)][ValidateSet('ExistingContainer','ExistingVolume','AmbientPort','RestoreFailure')][string]$Scenario,[Parameter(Mandatory)][string]$FixtureRoot)
$ErrorActionPreference='Stop'
# All Docker calls are mocks. Only synthetic files under FixtureRoot are written.
$env:OS='Windows_NT'
New-Item -ItemType Directory -Force $FixtureRoot | Out-Null
Copy-Item (Join-Path $Source '*.ps1') $FixtureRoot
New-Item -ItemType Directory -Force (Join-Path $FixtureRoot '.windows-phase2') | Out-Null
Set-Content (Join-Path $FixtureRoot '.windows-phase2/.env.local') 'synthetic config only'
$global:SafetyDockerOperations=@()
function docker {
    $global:LASTEXITCODE=0
    if ($args[0] -eq 'compose') {
        if ($args -contains 'ps') { if ($Scenario -eq 'ExistingContainer') { 'existing-container' }; return }
        if ($args -contains 'create') { $global:SafetyDockerOperations+='create'; throw 'synthetic restore create failure' }
        if ($args -contains 'stop') {
            $index=[Array]::IndexOf($args,'-p')
            if ($index -lt 0 -or $args[$index+1] -notmatch '^new-api-win-restore-') { throw 'Rollback targeted the source project' }
            $global:SafetyDockerOperations+='stop'; return
        }
        throw 'Unexpected compose mutation'
    }
    if ($args[0] -eq 'volume' -and $args[1] -eq 'ls') { if ($Scenario -eq 'ExistingVolume') { 'new-api-win-phase2-rc47_new-api-phase2-data' }; return }
    throw 'Unexpected Docker command'
}
function Get-NetTCPConnection { param($LocalPort,$State,$ErrorAction); return @() }
$originalPort=$env:NEW_API_PORT
$expected=if ($Scenario -eq 'AmbientPort') { $env:NEW_API_PORT='3067'; 'Ambient NEW_API_PORT' } else { $env:NEW_API_PORT=$null; 'Existing phase2' }
try {
    if ($Scenario -eq 'RestoreFailure') {
        $backup=Join-Path $FixtureRoot 'backup'
        New-Item -ItemType Directory -Force (Join-Path $backup 'data'),(Join-Path $backup 'phase2') | Out-Null
        foreach ($name in @('.env.local','fingerprint.json','compose.yaml','database-summary.json')) { Set-Content (Join-Path $backup $name) 'synthetic fixture' }
        Set-Content (Join-Path $backup 'data/one-api.db') 'synthetic database; never opened'
        Copy-Item (Join-Path $Source 'windows-compose.yaml') (Join-Path $FixtureRoot 'windows-compose.yaml')
        Copy-Item (Join-Path $Source 'windows-compose.yaml') (Join-Path $backup 'compose.yaml')
        Copy-Item (Join-Path $FixtureRoot '*.ps1') $backup
        # Restore validates source helper hashes before invoking Docker.
        $phase2=Join-Path $FixtureRoot '../phase2'
        New-Item -ItemType Directory -Force $phase2 | Out-Null
        foreach ($name in @('mock.py','viewer.py','verify.py','db_check.py')) { Set-Content (Join-Path $phase2 $name) 'synthetic fixture'; Copy-Item (Join-Path $phase2 $name) (Join-Path $backup 'phase2') }
        Get-ChildItem $backup -Recurse -File -Force | ForEach-Object { @{name=$_.FullName.Substring($backup.Length+1);sha256=(Get-FileHash $_.FullName).Hash} } | ConvertTo-Json | Set-Content (Join-Path $backup 'sha256.json')
        $message=$null
        try { & (Join-Path $FixtureRoot 'windows-restore.ps1') -BackupPath $backup } catch { $message=$_.Exception.Message }
        if ($message -notmatch 'synthetic restore create failure') { throw "Wrong restore error: $message" }
        if (($global:SafetyDockerOperations -join ',') -ne 'create,stop') { throw 'Restore rollback did not stop only the new project' }
        if ($null -ne $env:NEW_API_PORT) { throw 'Restore environment override not reverted' }
    } else {
        $message=$null
        try { & (Join-Path $FixtureRoot 'windows-deploy.ps1') -Action Start } catch { $message=$_.Exception.Message }
        if ($message -notmatch $expected) { throw "Startup guard failed: $message" }
        if ($global:SafetyDockerOperations.Count) { throw 'Guard allowed a mutation' }
    }
} finally { $env:NEW_API_PORT=$originalPort }
Write-Output "MOCK PASS $Scenario; no Docker daemon contacted"
