[CmdletBinding()]
param([ValidateSet('Init','Start','Test','Inspect','Stop','Restart')][string]$Action='Init')
. (Join-Path $PSScriptRoot 'windows-common.ps1')
if ($env:OS -ne 'Windows_NT') { throw 'Run on Windows 11 with Docker Desktop' }
switch ($Action) {
    Init {
        & (Join-Path $PSScriptRoot 'windows-preflight.ps1')
        if ($LASTEXITCODE -ne 0) { throw 'Preflight failed' }
        if (Test-Path $script:Phase2Env) { throw 'Local configuration exists; preserved without overwrite' }
        New-Item -ItemType Directory -Force $script:Phase2Local | Out-Null
        $rng=[Security.Cryptography.RandomNumberGenerator]::Create()
        try {
            $bytes=New-Object byte[] 32; $rng.GetBytes($bytes); $secret=([BitConverter]::ToString($bytes)).Replace('-','')
            $rng.GetBytes($bytes); $password=([BitConverter]::ToString($bytes)).Replace('-','')+'Aa1!'
        } finally { $rng.Dispose() }
        [IO.File]::WriteAllText($script:Phase2Env,"SESSION_SECRET=$secret`nTEST_ADMIN_PASSWORD=$password`nNEW_API_PORT=3077`n",(New-Object Text.UTF8Encoding($false)))
        Write-Host 'Local-only secrets created. Never commit .windows-phase2. Keep this directory on an encrypted private disk.'
    }
    Start {
        if (-not (Test-Path $script:Phase2Env)) { throw 'Run Init first' }
        # First start must never adopt an existing project or data volume.
        $existing=@(Invoke-Phase2Compose -Arguments @('ps','--all','--quiet'))
        $volumes=@(& docker volume ls --format '{{.Name}}')
        if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect existing Docker volumes' }
        if ($existing.Count -or $volumes -contains ($script:Phase2Project+'_new-api-phase2-data')) { throw 'Existing phase2 containers or database volume found. Preserved without startup/recreation; use Inspect/Restart only for your already initialized environment.' }
        if ($env:NEW_API_PORT -and $env:NEW_API_PORT -ne '3077') { throw 'Ambient NEW_API_PORT must not override the main test port 3077' }
        Invoke-Phase2Compose -Arguments @('config','--quiet')
        Invoke-Phase2Compose -Arguments @('pull')
        Invoke-Phase2Compose -Arguments @('up','-d','--wait')
        Write-Host 'Local mock-only test console: http://127.0.0.1:3077. Run Test only once on the fresh dedicated database.'
    }
    Test {
        New-Item -ItemType Directory -Force (Join-Path $script:Phase2Local 'results') | Out-Null
        Invoke-Phase2Compose -Arguments @('exec','-T','mock','python','/phase2/verify.py') | Tee-Object (Join-Path $script:Phase2Local ('results/protocol-'+(Get-Date -Format 'yyyyMMdd-HHmmss')+'.jsonl'))
    }
    Inspect { Get-Phase2Fingerprint }
    Stop { Invoke-Phase2Compose -Arguments @('stop') }
    Restart { Invoke-Phase2Compose -Arguments @('restart'); Invoke-Phase2Compose -Arguments @('up','-d','--wait'); Get-Phase2Fingerprint }
}
