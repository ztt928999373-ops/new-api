param([Parameter(Mandatory)][string]$ExpectedSha)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = (Get-Location).Path
$logs = Join-Path $repo 'windows-acceptance-logs'
New-Item -ItemType Directory -Force -Path $logs | Out-Null
trap {
  @{message=$_.Exception.Message;position=$_.InvocationInfo.PositionMessage;stack=$_.ScriptStackTrace} | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $logs 'script-error.json')
  throw
}
$actual = (git rev-parse HEAD).Trim()
if ($actual -ne $ExpectedSha -or $actual -notmatch '^[0-9a-f]{40}$') { throw 'Checkout SHA mismatch' }
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
@{sha=$actual; os=[Environment]::OSVersion.VersionString; user=$identity.Name; sid=$identity.User.Value; go=(& go version); started=(Get-Date -Format o)} | ConvertTo-Json | Set-Content (Join-Path $logs 'environment.json')
# No application configuration or database is taken from a developer machine.
foreach ($name in @('SQL_DSN','LOG_SQL_DSN','REDIS_CONN_STRING','SUPPLIER_CREDENTIAL_KEYRING_PATH')) { Remove-Item "Env:$name" -ErrorAction SilentlyContinue }
$env:GOWORK='off'
$env:CGO_ENABLED='0'
if (!(Test-Path web/dist/index.html)) { throw 'Production frontend build is required' }
$binary = Join-Path $env:RUNNER_TEMP 'batchd-api.exe'
& go build -ldflags "-X github.com/QuantumNous/new-api/common.Version=$actual" -o $binary . *> (Join-Path $logs 'build.log')
if ($LASTEXITCODE -ne 0) { throw 'Application build failed' }
$root = "C:\SuanjieBatchD-$env:GITHUB_RUN_ID-$env:GITHUB_RUN_ATTEMPT"
if (Test-Path -LiteralPath $root) { throw 'Refusing to reuse an existing test root' }
Get-Acl -LiteralPath 'C:\' | Format-List Owner,AreAccessRulesProtected,AccessToString | Out-File (Join-Path $logs 'volume-acl.txt')
# The existing product creates and validates the directory and every ancestor.
# An incompatible runner fails here. No ancestor ACL is changed or whitelisted.
& $binary ops private-dir --state $root *> (Join-Path $logs 'private-root.log')
if ($LASTEXITCODE -ne 0) { throw 'Runner cannot provide a product-compliant private root' }
Get-Acl -LiteralPath $root | Format-List Owner,AreAccessRulesProtected,AccessToString | Out-File (Join-Path $logs 'root-acl.txt')
foreach ($name in @('temp','go-build','go-tmp','db-backups','key-backups')) {
  & $binary ops private-dir --state (Join-Path $root $name) *> (Join-Path $logs "private-$name.log")
  if ($LASTEXITCODE -ne 0) { throw "Private directory failed: $name" }
}
$env:TEMP=Join-Path $root 'temp'; $env:TMP=$env:TEMP
$env:GOCACHE=Join-Path $root 'go-build'; $env:GOTMPDIR=Join-Path $root 'go-tmp'
$env:SESSION_SECRET='synthetic-batchd-windows-session-only'
$results=[Collections.Generic.List[object]]::new()
function GoTest([string]$Name,[string[]]$Arguments) {
  Write-Host "Starting Go phase: $Name"
  $timer=[Diagnostics.Stopwatch]::StartNew()
  & go test -json @Arguments *> (Join-Path $logs "$Name.jsonl")
  $code=$LASTEXITCODE
  $results.Add(@{name=$Name;exit=$code;seconds=$timer.Elapsed.TotalSeconds})
  $results | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $logs 'test-commands.json')
  Write-Host "Finished Go phase: $Name (exit $code)"
}
# Each command runs once. Failures are preserved and never silently retried.
GoTest 'five-packages' @('./internal/localops','./pkg/credentialcrypto','./service/credentials','./service','./router','-count=1','-timeout=5m')
GoTest 'controller-timeout-probe' @('./controller','-run','^TestManageUserQuotaTargetsAndWalletBounds$','-count=1','-timeout=30s')
if ($results[$results.Count-1].exit -ne 0) { throw 'Single-test timeout probe failed; full controller run was not started' }
# The first runner completed 445 top-level tests before the aggregate 300s cap;
# it was still making progress, with no cleanup errors. Keep a bounded 360s
# package budget only after the isolated 30s probe succeeds. Request-level
# deadlines and all assertions remain unchanged.
GoTest 'controller-full' @('./controller','-count=1','-timeout=6m')
GoTest 'http2' @('./relay/channel','-run','^TestUpstreamGetBody_HTTP2','-count=20','-timeout=2m')
GoTest 'sqlite-cleanup' @('./controller','-run','^(TestAuditSQLiteFixtureReleasesWALFiles|TestAuditDatabaseMatrix)$','-count=1','-timeout=60s')
$results | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $logs 'test-commands.json')
$source=Join-Path $root 'source'; $restored=Join-Path $root 'restored'
function InvokeTestBinary([string]$Name,[string[]]$Arguments) {
  Write-Host "Starting actual CLI phase: $Name"
  & $binary @Arguments *> (Join-Path $logs "$Name.log")
  if ($LASTEXITCODE -ne 0) { throw "CLI phase failed: $Name" }
}
InvokeTestBinary 'init' @('ops','init','--state',$source,'--environment','test')
InvokeTestBinary 'check-source' @('ops','check','--state',$source)
function StartApi([string]$State,[string]$Name) {
  $child=Start-Process -FilePath $binary -ArgumentList @('ops','serve','--state',$State,'--port','5536') -PassThru -WindowStyle Hidden -RedirectStandardOutput (Join-Path $logs "$Name.stdout.log") -RedirectStandardError (Join-Path $logs "$Name.stderr.log")
  for ($attempt=0;$attempt -lt 60;$attempt++) {
    if ($child.HasExited) { throw 'API exited before becoming ready' }
    try { $r=Invoke-RestMethod 'http://127.0.0.1:5536/api/setup' -TimeoutSec 1; if ($r.success) { return $child } } catch {}
    Start-Sleep -Milliseconds 250
  }
  Stop-Process -Id $child.Id -Force
  throw 'API readiness deadline exceeded'
}
$session=$null; $child=$null
function Api([string]$Method,[string]$Path,$Body=$null) {
  $requestOptions=@{Method=$Method;Uri="http://127.0.0.1:5536$Path";TimeoutSec=10;ContentType='application/json'}
  if ($null -ne $session) { $requestOptions.Headers=@{Authorization="Bearer $session"} }
  if ($null -ne $Body) { $requestOptions.Body=($Body | ConvertTo-Json -Depth 8 -Compress) }
  $response=Invoke-RestMethod @requestOptions
  if (!$response.success) { throw "Management API failed: $Path" }
  if (($response | ConvertTo-Json -Depth 20 -Compress).Contains('synthetic-batchd-key-')) { throw 'Supplier key leaked in a management response' }
  return $response
}
function Login {
  $script:session=(Api 'POST' '/api/user/login' @{username='batchd-root';password='Synthetic-BatchD-Password!'}).data.access_token
  if (!$session) { throw 'Login did not return a session token' }
}
try {
  $child=StartApi $source 'source'
  $null=Api 'POST' '/api/setup' @{username='batchd-root';password='Synthetic-BatchD-Password!';confirmPassword='Synthetic-BatchD-Password!';SelfUseModeEnabled=$false;DemoSiteEnabled=$false}
  Login
  $head=(Api 'POST' '/api/channel/credentials' @{key='synthetic-batchd-key-one'}).data
  $id=$head.id
  $head=(Api 'PUT' "/api/channel/credentials/$id" @{key='synthetic-batchd-key-two';revision=$head.revision}).data
  if ($head.version -ne 2) { throw 'Credential replacement version mismatch' }
  $null=Api 'POST' "/api/channel/credentials/$id/status" @{status='disabled';revision=$head.revision}
  $head=(Api 'GET' "/api/channel/credentials/$id").data
  if ($head.status -ne 'disabled') { throw 'Credential did not disable' }
  $null=Api 'POST' "/api/channel/credentials/$id/status" @{status='active';revision=$head.revision}
  $head=(Api 'GET' "/api/channel/credentials/$id").data
  $null=Api 'POST' "/api/channel/credentials/$id/status" @{status='revoked';revision=$head.revision}
  $db=Join-Path $root 'db-backups/snapshot.sqlite'; $ring=Join-Path $root 'key-backups/snapshot.ring'
  & $binary ops backup --state $source --database-backup $db --key-backup $ring *> (Join-Path $logs 'running-backup-rejected.log')
  if ($LASTEXITCODE -eq 0 -or (Test-Path $db) -or (Test-Path $ring)) { throw 'Running cold backup did not fail closed' }
  Stop-Process -Id $child.Id -Force; $child.WaitForExit(); $child=$null
  InvokeTestBinary 'backup' @('ops','backup','--state',$source,'--database-backup',$db,'--key-backup',$ring)
  InvokeTestBinary 'restore' @('ops','restore','--state',$restored,'--database-backup',$db,'--key-backup',$ring)
  InvokeTestBinary 'check-restored' @('ops','check','--state',$restored)
  $session=$null; $child=StartApi $restored 'restored'; Login
  $metadata=Api 'GET' "/api/channel/credentials/$id"
  if ($metadata.data.status -ne 'revoked' -or $metadata.data.version -ne 2) { throw 'Restored credential metadata changed' }
  $null=Api 'POST' '/api/channel/credentials' @{key='synthetic-batchd-key-three'}
  $env:BATCHD_BROWSER_TOOLS=Join-Path $root 'browser-tools'
  & npm install --prefix $env:BATCHD_BROWSER_TOOLS --ignore-scripts --no-audit --no-fund playwright-core@1.62.1 *> (Join-Path $logs 'browser-tools.log')
  if ($LASTEXITCODE -ne 0) { throw 'Browser test dependency installation failed' }
  $chromePaths=@('C:\Program Files\Google\Chrome\Application\chrome.exe','C:\Program Files (x86)\Google\Chrome\Application\chrome.exe')
  $chromeCandidates=@($chromePaths | Where-Object {Test-Path -LiteralPath $_})
  if ($chromeCandidates.Count -eq 0) { throw 'Runner does not provide Chrome' }
  $env:BATCHD_CHROME=$chromeCandidates[0]
  $env:BATCHD_EXPECTED_SHA=$actual; $env:BATCHD_LOGS=$logs
  & node .github/scripts/windows-management-browser.cjs *> (Join-Path $logs 'management-browser.log')
  if ($LASTEXITCODE -ne 0) { throw 'Production management browser verification failed' }
  @{create=$true;replace=$true;disable=$true;reactivate=$true;revoke=$true;restoreRetainsRevocation=$true;restoredRingWrite=$true;runningBackupRejected=$true} | ConvertTo-Json | Set-Content (Join-Path $logs 'actual-api-recovery.json')
} finally {
  if ($null -ne $child -and !$child.HasExited) { Stop-Process -Id $child.Id -Force; $child.WaitForExit() }
  $session=$null
}
InvokeTestBinary 'final-check' @('ops','check','--state',$restored)
$leaks=Get-ChildItem $logs -Filter '*.stdout.log' | Select-String 'synthetic-batchd-key-'
$leaks+=Get-ChildItem $logs -Filter '*.stderr.log' | Select-String 'synthetic-batchd-key-'
if ($leaks) { throw 'Supplier key leaked in application logs' }
if (@($results | Where-Object exit -ne 0).Count -gt 0) { throw 'One or more recorded Go test commands failed' }
@{sha=$actual;completed=(Get-Date -Format o);result='pass';root=$root;scope='Windows ACL, six packages, real API credential lifecycle and actual CLI recovery; Mock relay/billing through existing router HTTP tests'} | ConvertTo-Json | Set-Content (Join-Path $logs 'acceptance.json')
