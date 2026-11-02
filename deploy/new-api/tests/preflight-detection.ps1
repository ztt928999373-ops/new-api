param([Parameter(Mandatory)][string]$Script,[Parameter(Mandatory)][ValidateSet('PerUser','AllUsers','ProcessOnly','ServerDown')][string]$Scenario,[Parameter(Mandatory)][string]$FixtureRoot)
$ErrorActionPreference='Stop'
# Pure mocks: no Docker executable or Windows services are invoked.
$env:OS='Windows_NT'
$env:ProgramFiles=Join-Path $FixtureRoot 'allusers'
$env:LOCALAPPDATA=Join-Path $FixtureRoot 'local'
$exe=if ($Scenario -eq 'AllUsers') { Join-Path $env:ProgramFiles 'Docker/Docker/Docker Desktop.exe' } else { Join-Path $env:LOCALAPPDATA 'Programs/DockerDesktop/Docker Desktop.exe' }
if ($Scenario -in @('PerUser','AllUsers')) { New-Item -ItemType Directory -Force (Split-Path $exe) | Out-Null; New-Item -ItemType File -Force $exe | Out-Null }
function Get-CimInstance { param($ClassName); [pscustomobject]@{Caption='Windows 11 (mock)';Version='10.0';BuildNumber='26100'} }
function Get-PSDrive { param($Name); [pscustomobject]@{Free=53.35GB;Used=100GB} }
function Get-Process { param($Name,$ErrorAction); [pscustomobject]@{ProcessName='Docker Desktop'} }
function Get-NetTCPConnection { param($LocalPort,$State,$ErrorAction); return @() }
function docker {
    $global:LASTEXITCODE=0
    if ($args[0] -eq 'version' -and $Scenario -eq 'ServerDown') { $global:LASTEXITCODE=1; return }
    if ($args[0] -eq 'info' -and $args[-1] -eq '{{.OSType}}') { 'linux'; return }
    if ($args[0] -notin @('version','compose','info','system','image','ps','volume')) { throw 'Unexpected Docker operation in preflight' }
    if ($args -contains 'prune' -or $args -contains 'up' -or $args -contains 'run' -or $args -contains 'pull') { throw 'Mutating Docker operation attempted' }
    'mock-readonly-docker-result'
}
& $Script
$actual=$LASTEXITCODE
$expected=if ($Scenario -eq 'ServerDown') { 1 } else { 0 }
if ($actual -ne $expected) { throw "Detection scenario $Scenario got $actual, expected $expected" }
Write-Output "MOCK PASS $Scenario (exit $actual)"
