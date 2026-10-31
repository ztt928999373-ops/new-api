[CmdletBinding()]
param([int[]]$Ports = @(3077,3078), [int]$MinimumFreeGB = 20)
$ErrorActionPreference = 'Stop'
$failed = $false
Write-Host 'Read-only Windows/Docker preflight. No cleanup, deletion, pull or start operations.'
Write-Host "Project directory: $PSScriptRoot"
if ($env:OS -ne 'Windows_NT') { throw 'Run this preflight on Windows 11, not in the Codex Linux environment.' }
$drive = Get-PSDrive -Name C
Write-Host ('C: free {0:N2} GB / used {1:N2} GB' -f ($drive.Free/1GB),($drive.Used/1GB))
if ($drive.Free -lt $MinimumFreeGB*1GB) { Write-Warning 'Insufficient C: free space; review Docker Desktop disk allocation manually.'; $failed=$true }
$desktop = Join-Path $env:ProgramFiles 'Docker\Docker\Docker Desktop.exe'
Write-Host "Docker Desktop executable installed: $(Test-Path $desktop)"
Write-Host "Docker Desktop process running: $([bool](Get-Process -Name 'Docker Desktop' -ErrorAction SilentlyContinue))"
Get-ChildItem $PSScriptRoot -Force | Select-Object Mode,Name
foreach ($port in $Ports) {
    $listeners = @(Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue)
    if ($listeners.Count) { Write-Warning "Port $port is occupied"; $listeners | Select-Object LocalAddress,LocalPort,OwningProcess }
    else { Write-Host "Port $port is free" }
}
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { Write-Warning 'Docker CLI missing; install Docker Desktop/WSL2 and reopen PowerShell.'; exit 1 }
docker version
if ($LASTEXITCODE -ne 0) { Write-Warning 'Docker daemon unavailable; start Docker Desktop and wait for Linux engine.'; exit 1 }
docker compose version
if ($LASTEXITCODE -ne 0) { $failed=$true }
$kind = docker info --format '{{.OSType}}'
if ($kind -ne 'linux') { Write-Warning 'Linux containers required'; $failed=$true }
docker info --format 'Engine={{.ServerVersion}} OS={{.OperatingSystem}} Arch={{.Architecture}} Driver={{.Driver}} DockerRoot={{.DockerRootDir}}'
docker system df
docker image ls --digests
docker ps -a --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}'
docker volume ls
docker compose ls
Write-Host 'Docker disk usage above is logical usage. Check actual VHDX location, free space and disk limit in Docker Desktop Settings > Resources > Disk.'
Write-Host 'No images, containers or volumes have been deleted. Do not send local inventory or credentials to a public issue.'
if ($failed) { exit 1 }
