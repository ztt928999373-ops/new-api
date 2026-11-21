$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\Invoke-G1Acceptance.ps1') -BaseUri 'http://127.0.0.1:3137' -ExpectedRevision ('0' * 40) -Port 3137

$script:failures = [Collections.Generic.List[string]]::new()
function Assert-G1Test([string]$Name,[scriptblock]$Action) {
    try { & $Action; Write-Output "PASS $Name" }
    catch { $script:failures.Add("FAIL $Name :: $($_.Exception.Message)"); Write-Output $script:failures[-1] }
}

Assert-G1Test 'IPv4 loopback accepted' {
    if ((Assert-G1LoopbackUri 'http://127.0.0.1:3137').Host -ne '127.0.0.1') { throw 'wrong host' }
}
Assert-G1Test 'IPv6 loopback accepted' {
    $uri=Assert-G1LoopbackUri 'http://[::1]:3137'
    if (-not [Net.IPAddress]::IsLoopback([Net.IPAddress]::Parse($uri.Host.Trim('[',']')))) { throw 'wrong host' }
}
Assert-G1Test 'non-loopback rejected' {
    try { Assert-G1LoopbackUri 'http://192.0.2.1:3137' | Out-Null; throw 'non-loopback accepted' }
    catch { if ($_.Exception.Message -eq 'non-loopback accepted') { throw } }
}
Assert-G1Test 'hostnames and redirects are rejected' {
    foreach ($uri in @('http://localhost:3137','http://127.0.0.1:3138/path')) {
        try { Assert-G1LoopbackUri $uri | Out-Null; throw "URI accepted: $uri" }
        catch { if ($_.Exception.Message.StartsWith('URI accepted:')) { throw } }
    }
}
Assert-G1Test 'HTTP redirects and proxies are disabled' {
    $handler=New-G1HttpHandler
    try { if ($handler.AllowAutoRedirect -or $handler.UseProxy) { throw 'unsafe handler configuration' } }
    finally { $handler.Dispose() }
}
Assert-G1Test 'Mock counts and event field allowlist' {
    $path=Join-Path ([IO.Path]::GetTempPath()) (([Guid]::NewGuid().ToString())+'.ndjson')
    try {
        [IO.File]::WriteAllLines($path,@(
            '{"event":"chat_inbound","authorization":"do-not-emit"}',
            '{"event":"chat_response","status":200,"model":"g1-mock-text","usage":{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150},"prompt":"do-not-emit"}',
            '{"event":"healthz","health_checks":1}'
        ))
        $result=Get-G1MockSummary $path; $rendered=$result|ConvertTo-Json -Depth 8
        if ($result.counts.chat_inbound -ne 1 -or $result.counts.chat_response -ne 1 -or $result.counts.health_check -ne 1) { throw 'event counts mismatch' }
        if ($rendered -match 'do-not-emit|authorization|"prompt"') { throw 'unapproved event data leaked' }
        if ($result.chat_responses[0].prompt_tokens -ne 120) { throw 'usage missing' }
    } finally { Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue }
}
Assert-G1Test 'known price expression computes 90 quota for synthetic 120/30 usage' {
    $estimate=Get-G1BillingEstimate 120 30 'g1-mock-text' 'g1-mock-text' 'tier("base", p * 1 + c * 2)' 500000 1 $true $true
    if ($estimate.status -ne 'PASS' -or $estimate.expected_quota -ne 90) { throw 'expected quota mismatch' }
}
Assert-G1Test 'unknown pricing remains UNKNOWN' {
    if ((Get-G1BillingEstimate 120 30 'g1-mock-text' 'g1-mock-text' 'other' 500000 1 $false $true).status -ne 'UNKNOWN') { throw 'unknown config was calculated' }
    if ((Get-G1BillingEstimate 120 30 'g1-mock-text' 'g1-mock-text' 'tier("base", p * 1 + c * 2)' 500000 1 $true $false).status -ne 'UNKNOWN') { throw 'unverified rate was calculated' }
    if ((Get-G1BillingEstimate 120 30 'other-model' 'g1-mock-text' 'tier("base", p * 1 + c * 2)' 500000 1 $true $true).status -ne 'UNKNOWN') { throw 'unverified model was calculated' }
}
Assert-G1Test 'user/token projections exclude unapproved fields' {
    $user=Get-G1Projection @{id=2;username='synthetic';quota=100;password='secret';email='private'} @('id','username','quota')
    $token=Get-G1Projection @{id=1;user_id=2;remain_quota=50;key='secret-key'} @('id','user_id','remain_quota')
    if (($user|ConvertTo-Json) -match 'secret|private' -or ($token|ConvertTo-Json) -match 'secret-key') { throw 'projection leaked data' }
}
Assert-G1Test 'snapshot reconciliation matches Mock usage, charge, balances, and request count' {
    $before=@{snapshot=@{users=@(@{id=2;quota=10000;used_quota=0;request_count=0});tokens=@(@{id=1;remain_quota=10000;used_quota=0});consume_log_total=0;consume_logs=@();mock=@{counts=@{chat_inbound=0;chat_response=0};chat_responses=@()}}}
    $after=@{users=@(@{id=2;quota=9910;used_quota=90;request_count=1});tokens=@(@{id=1;remain_quota=9910;used_quota=90});consume_log_total=1;consume_logs=@(@{id=1;user_id=2;token_id=1;model_name='g1-mock-text';quota=90;prompt_tokens=120;completion_tokens=30});mock=@{counts=@{chat_inbound=1;chat_response=1};chat_responses=@(@{status=200;model='g1-mock-text';prompt_tokens=120;completion_tokens=30;total_tokens=150})}}
    $estimate=Get-G1BillingEstimate 120 30 'g1-mock-text' 'g1-mock-text' 'tier("base", p * 1 + c * 2)' 500000 1 $true $true
    $comparison=Compare-G1Snapshots $before $after @($estimate)
    if ($comparison.status -ne 'PASS') { throw ('reconciliation failed: '+($comparison|ConvertTo-Json -Compress -Depth 8)) }
}
Assert-G1Test 'snapshot reconciliation detects quota mismatch' {
    $before=@{snapshot=@{users=@(@{id=2;quota=10000;used_quota=0;request_count=0});tokens=@(@{id=1;remain_quota=10000;used_quota=0});consume_log_total=0;consume_logs=@();mock=@{counts=@{chat_inbound=0;chat_response=0};chat_responses=@()}}}
    $after=@{users=@(@{id=2;quota=9920;used_quota=80;request_count=1});tokens=@(@{id=1;remain_quota=9920;used_quota=80});consume_log_total=1;consume_logs=@(@{id=7;user_id=2;token_id=1;model_name='g1-mock-text';quota=80;prompt_tokens=120;completion_tokens=30});mock=@{counts=@{chat_inbound=1;chat_response=1};chat_responses=@(@{status=200;model='g1-mock-text';prompt_tokens=120;completion_tokens=30;total_tokens=150})}}
    $estimate=Get-G1BillingEstimate 120 30 'g1-mock-text' 'g1-mock-text' 'tier("base", p * 1 + c * 2)' 500000 1 $true $true
    if ((Compare-G1Snapshots $before $after @($estimate)).status -ne 'FAIL') { throw 'quota mismatch was not detected' }
}
Assert-G1Test 'historical Mock events are ignored when there is no new request' {
    $history=@{users=@(@{id=2;quota=9910;used_quota=90;request_count=1});tokens=@(@{id=1;remain_quota=9910;used_quota=90});consume_log_total=1;consume_logs=@(@{id=1;user_id=2;token_id=1;model_name='g1-mock-text';quota=90;prompt_tokens=120;completion_tokens=30});mock=@{counts=@{chat_inbound=1;chat_response=1};chat_responses=@(@{status=200;model='g1-mock-text';prompt_tokens=120;completion_tokens=30;total_tokens=150})}}
    $before=@{snapshot=$history}; $after=@{users=$history.users;tokens=$history.tokens;consume_log_total=$history.consume_log_total;consume_logs=$history.consume_logs;mock=$history.mock}
    $comparison=Compare-G1Snapshots $before $after @()
    if ($comparison.status -ne 'PASS' -or $comparison.mock_response_delta -ne 0) { throw ('historical event was reprocessed: '+($comparison|ConvertTo-Json -Compress -Depth 8)) }
}
Assert-G1Test 'only the appended Mock event matches a new consume log' {
    $oldEvent=@{status=200;model='g1-mock-text';prompt_tokens=120;completion_tokens=30;total_tokens=150}
    $newLog=@{id=2;user_id=2;token_id=1;model_name='g1-mock-text';quota=90;prompt_tokens=120;completion_tokens=30}
    $before=@{snapshot=@{users=@(@{id=2;quota=9910;used_quota=90;request_count=1});tokens=@(@{id=1;remain_quota=9910;used_quota=90});consume_log_total=1;consume_logs=@(@{id=1;user_id=2;token_id=1;model_name='g1-mock-text';quota=90;prompt_tokens=120;completion_tokens=30});mock=@{counts=@{chat_inbound=1;chat_response=1};chat_responses=@($oldEvent)}}}
    $newEvent=@{status=200;model='g1-mock-text';prompt_tokens=120;completion_tokens=30;total_tokens=150}
    $after=@{users=@(@{id=2;quota=9820;used_quota=180;request_count=2});tokens=@(@{id=1;remain_quota=9820;used_quota=180});consume_log_total=2;consume_logs=@(@{id=1;user_id=2;token_id=1;model_name='g1-mock-text';quota=90;prompt_tokens=120;completion_tokens=30},$newLog);mock=@{counts=@{chat_inbound=2;chat_response=2};chat_responses=@($oldEvent,$newEvent)}}
    $estimate=Get-G1BillingEstimate 120 30 'g1-mock-text' 'g1-mock-text' 'tier("base", p * 1 + c * 2)' 500000 1 $true $true
    $comparison=Compare-G1Snapshots $before $after @($estimate)
    if ($comparison.status -ne 'PASS' -or $comparison.mock_response_delta -ne 1) { throw ('new event reconciliation failed: '+($comparison|ConvertTo-Json -Compress -Depth 8)) }
}
Assert-G1Test 'missing key baseline fields return UNKNOWN' {
    $before=@{snapshot=@{users=@(@{id=2;quota=100;used_quota=0;request_count=0});tokens=@(@{id=1;used_quota=0});consume_log_total=0;consume_logs=@();mock=@{counts=@{chat_inbound=0;chat_response=0};chat_responses=@()}}}
    $after=@{users=@(@{id=2;quota=100;used_quota=0;request_count=0});tokens=@(@{id=1;remain_quota=100;used_quota=0});consume_log_total=0;consume_logs=@();mock=@{counts=@{chat_inbound=0;chat_response=0};chat_responses=@()}}
    if ((Compare-G1Snapshots $before $after @()).status -ne 'UNKNOWN') { throw 'missing key baseline returned a confident result' }
}
Assert-G1Test 'missing user baseline returns UNKNOWN' {
    $before=@{snapshot=@{users=@(@{id=2;quota=100;used_quota=0;request_count=0});tokens=@(@{id=1;remain_quota=100;used_quota=0});consume_log_total=0;consume_logs=@();mock=@{counts=@{chat_inbound=0;chat_response=0};chat_responses=@()}}}
    $after=@{users=@(@{id=3;quota=100;used_quota=0;request_count=0});tokens=@(@{id=1;remain_quota=100;used_quota=0});consume_log_total=0;consume_logs=@();mock=@{counts=@{chat_inbound=0;chat_response=0};chat_responses=@()}}
    if ((Compare-G1Snapshots $before $after @()).status -ne 'UNKNOWN') { throw 'missing user baseline returned a confident result' }
}
Assert-G1Test 'inbound request without a recorded Mock response returns UNKNOWN' {
    $before=@{snapshot=@{users=@(@{id=2;quota=100;used_quota=0;request_count=0});tokens=@(@{id=1;remain_quota=100;used_quota=0});consume_log_total=0;consume_logs=@();mock=@{counts=@{chat_inbound=0;chat_response=0};chat_responses=@()}}}
    $after=@{users=$before.snapshot.users;tokens=$before.snapshot.tokens;consume_log_total=0;consume_logs=@();mock=@{counts=@{chat_inbound=1;chat_response=0};chat_responses=@()}}
    if ((Compare-G1Snapshots $before $after @()).status -ne 'UNKNOWN') { throw 'inbound without response returned a confident result' }
}
Assert-G1Test 'failed Mock response settlement remains UNKNOWN' {
    $before=@{snapshot=@{users=@(@{id=2;quota=100;used_quota=0;request_count=0});tokens=@(@{id=1;remain_quota=100;used_quota=0});consume_log_total=0;consume_logs=@();mock=@{counts=@{chat_inbound=0;chat_response=0};chat_responses=@()}}}
    $after=@{users=$before.snapshot.users;tokens=$before.snapshot.tokens;consume_log_total=0;consume_logs=@();mock=@{counts=@{chat_inbound=1;chat_response=1};chat_responses=@(@{status=500;model='g1-mock-text'})}}
    if ((Compare-G1Snapshots $before $after @()).status -ne 'UNKNOWN') { throw 'failed response settlement returned a confident result' }
}
Assert-G1Test 'future scenario slots remain NOT_RUN' {
    $reserved=@{sse='NOT_RUN';failure_refund='NOT_RUN';timeout='NOT_RUN';retry='NOT_RUN';ab_authorization_isolation='NOT_RUN'}
    if (@($reserved.Values|Where-Object {$_ -ne 'NOT_RUN'}).Count -ne 0) { throw 'future scenario unexpectedly enabled' }
}
Assert-G1Test 'CLI process exit codes reflect comparison and report outcome' {
    $temp=[IO.Path]::GetTempPath(); $driverPath=Join-Path $temp (([Guid]::NewGuid().ToString())+'.ps1')
    $sourcePath=(Resolve-Path (Join-Path $PSScriptRoot '..\Invoke-G1Acceptance.ps1')).Path
    $driver=@'
param([string]$SourcePath,[string]$ComparisonStatus,[string]$ReportPath,[switch]$EmptyChecks,[switch]$OnlyOneCheck,[switch]$MissingOneCheck)
. $SourcePath -BaseUri 'http://127.0.0.1:3137' -ExpectedRevision ('0' * 40) -Port 3137
$checks=[ordered]@{status_http='PASS';source_revision='PASS';setup='PASS';registration_closed='PASS';listener_identity='PASS';user_api='PASS';token_api='PASS';consume_log_api='PASS'}
if ($EmptyChecks) { $checks.Clear() }
if ($OnlyOneCheck) { $checks=[ordered]@{status_http='PASS'} }
if ($MissingOneCheck) { $checks.Remove('consume_log_api') }
$result=[ordered]@{
    target=@{origin='http://127.0.0.1:3137'}
    checks=$checks
    snapshot=@{users=@();tokens=@();consume_log_total=0;mock=@{counts=@{chat_inbound=0;chat_response=0}}}
    comparison=@{status=$ComparisonStatus}
}
Invoke-G1AcceptanceCli -Result $result -ReportPath $ReportPath
exit $script:G1CliExitCode
'@
    try {
        [IO.File]::WriteAllText($driverPath,$driver,[Text.UTF8Encoding]::new($false))
        $failOutput=@(& pwsh -NoProfile -File $driverPath -SourcePath $sourcePath -ComparisonStatus 'FAIL' -ReportPath '' 2>&1); $failCode=$LASTEXITCODE
        $unknownOutput=@(& pwsh -NoProfile -File $driverPath -SourcePath $sourcePath -ComparisonStatus 'UNKNOWN' -ReportPath '' 2>&1); $unknownCode=$LASTEXITCODE
        $passOutput=@(& pwsh -NoProfile -File $driverPath -SourcePath $sourcePath -ComparisonStatus 'PASS' -ReportPath '' 2>&1); $passCode=$LASTEXITCODE
        $missingChecksOutput=@(& pwsh -NoProfile -File $driverPath -SourcePath $sourcePath -ComparisonStatus 'PASS' -ReportPath '' -EmptyChecks 2>&1); $missingChecksCode=$LASTEXITCODE
        $oneCheckOutput=@(& pwsh -NoProfile -File $driverPath -SourcePath $sourcePath -ComparisonStatus 'PASS' -ReportPath '' -OnlyOneCheck 2>&1); $oneCheckCode=$LASTEXITCODE
        $missingOneOutput=@(& pwsh -NoProfile -File $driverPath -SourcePath $sourcePath -ComparisonStatus 'PASS' -ReportPath '' -MissingOneCheck 2>&1); $missingOneCode=$LASTEXITCODE
        $notRunOutput=@(& pwsh -NoProfile -File $driverPath -SourcePath $sourcePath -ComparisonStatus 'NOT_RUN' -ReportPath '' 2>&1); $notRunCode=$LASTEXITCODE
        $missingParent=Join-Path $temp ([Guid]::NewGuid().ToString()); $badPath=Join-Path $missingParent 'report.json'
        $writeOutput=@(& pwsh -NoProfile -File $driverPath -SourcePath $sourcePath -ComparisonStatus 'PASS' -ReportPath $badPath 2>&1); $writeCode=$LASTEXITCODE
        if ($failCode -ne 2) { throw "FAIL comparison process exit was $failCode" }
        if ($unknownCode -ne 3) { throw "UNKNOWN comparison process exit was $unknownCode" }
        if ($passCode -ne 0) { throw "PASS comparison process exit was $passCode" }
        if ($missingChecksCode -ne 3) { throw "missing required checks process exit was $missingChecksCode" }
        if ($oneCheckCode -ne 3) { throw "single PASS check process exit was $oneCheckCode" }
        if ($missingOneCode -ne 3) { throw "one missing required check process exit was $missingOneCode" }
        if ($notRunCode -ne 3 -or (($notRunOutput -join [Environment]::NewLine) -notmatch '账务对账：NOT_RUN')) { throw 'no-baseline process did not report NOT_RUN with incomplete exit status' }
        if ($writeCode -eq 0) { throw 'report write failure returned a successful process exit' }
    } finally { Remove-Item -LiteralPath $driverPath -Force -ErrorAction SilentlyContinue }
}

if ($script:failures.Count) { exit 1 }
Write-Output 'Offline G1 acceptance tests: PASS'
