[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$BaseUri,
    [Parameter(Mandatory=$true)][ValidatePattern('^[0-9a-fA-F]{40}$')][string]$ExpectedRevision,
    [Parameter(Mandatory=$true)][ValidateRange(1,65535)][int]$Port,
    [string]$ExpectedExeSha256,
    [string]$MockEventsPath,
    [int[]]$UserIds = @(),
    [hashtable]$TokenIdsByUser = @{},
    [SecureString]$AdminBearer,
    [hashtable]$UserBearerById = @{},
    [string]$BaselineJsonPath,
    [string]$OutputPath,
    [string]$PricingModel,
    [string]$PricingExpression,
    [decimal]$QuotaPerUsd = 0,
    [decimal]$GroupRatio = 0,
    [switch]$NoAdditionalChargesVerified
)

function Assert-G1LoopbackUri([string]$Value) {
    $uri=$null
    if (-not [Uri]::TryCreate($Value,[UriKind]::Absolute,[ref]$uri)) { throw 'BaseUri is invalid.' }
    if ($uri.Scheme -notin @('http','https') -or $uri.UserInfo -or $uri.Query -or $uri.Fragment -or $uri.AbsolutePath -notin @('/','')) { throw 'BaseUri must be a plain HTTP(S) origin.' }
    $ip=$null
    if (-not [Net.IPAddress]::TryParse($uri.Host,[ref]$ip) -or $ip.ToString() -notin @('127.0.0.1','::1')) { throw 'Only literal 127.0.0.1 or ::1 loopback addresses are allowed.' }
    if ($uri.Port -ne $Port) { throw 'URI port does not match Port.' }
    return $uri
}

function Convert-G1SecureString([SecureString]$Value) {
    if ($null -eq $Value) { return $null }
    $ptr=[IntPtr]::Zero
    try { $ptr=[Runtime.InteropServices.Marshal]::SecureStringToBSTR($Value); return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr) }
    finally { if ($ptr -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr) } }
}

function New-G1HttpHandler {
    $handler=[Net.Http.HttpClientHandler]::new()
    $handler.AllowAutoRedirect=$false
    $handler.UseProxy=$false
    return $handler
}

function Invoke-G1LocalGet([Uri]$Origin,[string]$Path,[SecureString]$Bearer) {
    $handler=New-G1HttpHandler
    $client=[Net.Http.HttpClient]::new($handler); $client.Timeout=[TimeSpan]::FromSeconds(15); $request=[Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::Get,[Uri]::new($Origin,$Path))
    try {
        if ($null -ne $Bearer) { $plain=Convert-G1SecureString $Bearer; try { $request.Headers.Authorization=[Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer',$plain) } finally { $plain=$null } }
        $response=$client.Send($request)
        try {
            $code=[int]$response.StatusCode
            if ($code -ge 300 -and $code -lt 400) { throw "HTTP $code redirect rejected; redirect was not followed." }
            if ($code -lt 200 -or $code -ge 300) { throw "HTTP $code from local read-only endpoint." }
            try { $json=ConvertFrom-Json -InputObject $response.Content.ReadAsStringAsync().GetAwaiter().GetResult() -AsHashtable -ErrorAction Stop } catch { throw 'Local endpoint returned invalid JSON.' }
            if ($json.ContainsKey('success') -and -not $json.success) { throw 'Local API application success=false.' }
            return [pscustomobject]@{ http_status=$code; success=[bool]$json.success; data=$json.data }
        } finally { $response.Dispose() }
    } finally { $request.Dispose(); $client.Dispose(); $handler.Dispose() }
}

function Get-G1Projection($Object,[string[]]$Fields) {
    $safe=[ordered]@{}
    foreach ($field in $Fields) { if ($Object.ContainsKey($field)) { $safe[$field]=$Object[$field] } }
    return $safe
}

function Get-G1LogProjection($Log) {
    $safe=Get-G1Projection $Log @('id','user_id','token_id','model_name','quota','prompt_tokens','completion_tokens','channel','type','created_at')
    if ($Log.request_id) {
        $bytes=[Text.Encoding]::UTF8.GetBytes([string]$Log.request_id)
        try { $safe.request_ref=[Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant() }
        finally { [Array]::Clear($bytes,0,$bytes.Length) }
    }
    return $safe
}

function Get-G1MockSummary([string]$Path) {
    if (-not $Path) { return @{ available=$false; reason='not_provided' } }
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return @{ available=$false; reason='file_not_found' } }
    if ((Get-Item -LiteralPath $Path).Length -gt 10MB) { throw 'Mock NDJSON exceeds 10 MiB read limit.' }
    $counts=@{chat_inbound=0;chat_response=0;health_check=0;other=0}; $responses=[Collections.Generic.List[object]]::new()
    foreach ($line in [IO.File]::ReadLines((Resolve-Path -LiteralPath $Path).Path)) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        try { $e=ConvertFrom-Json -InputObject $line -AsHashtable -ErrorAction Stop } catch { throw 'Mock NDJSON invalid line; content suppressed.' }
        switch ($e.event) {
            'chat_inbound' { $counts.chat_inbound++ }
            'chat_response' {
                $counts.chat_response++; $safe=[ordered]@{status=$e.status}
                if ($e.model) { $safe.model=$e.model }
                if ($e.usage -is [Collections.IDictionary]) { foreach ($k in @('prompt_tokens','completion_tokens','total_tokens')) { if ($e.usage.Contains($k)) { $safe[$k]=$e.usage[$k] } } }
                $responses.Add($safe)
            }
            { $_ -in @('healthz','health_check') } { $counts.health_check++ }
            default { $counts.other++ }
        }
    }
    return @{available=$true;counts=$counts;chat_responses=@($responses)}
}

function Get-G1ListenerEvidence([int]$ListenPort,[string]$ExpectedHash) {
    if (-not (Get-Command Get-NetTCPConnection -ErrorAction SilentlyContinue)) { return @{status='UNKNOWN';reason='listener query unavailable';listeners=@()} }
    try { $rows=@(Get-NetTCPConnection -State Listen -LocalPort $ListenPort -ErrorAction Stop) } catch { return @{status='UNKNOWN';reason='listener query unavailable';listeners=@()} }
    $listeners=@(); $ok=$rows.Count -gt 0
    foreach ($row in $rows) {
        $ip=$null; $loop=[Net.IPAddress]::TryParse([string]$row.LocalAddress,[ref]$ip) -and [Net.IPAddress]::IsLoopback($ip); $path=$null; $hash=$null
        try { $path=(Get-Process -Id $row.OwningProcess -ErrorAction Stop).Path; if ($path -and $ExpectedHash) { $hash=(Get-FileHash -LiteralPath $path -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant() } } catch { }
        $identity=if (-not $ExpectedHash) {'UNKNOWN'} elseif ($hash -and $hash -eq $ExpectedHash.ToLowerInvariant()) {'PASS'} else {'FAIL'}
        if (-not $loop -or $identity -ne 'PASS') { $ok=$false }
        $listeners+=@{address=[string]$row.LocalAddress;pid=[int]$row.OwningProcess;process_path=$path;exe_sha256=$hash;loopback=$loop;identity=$identity}
    }
    $hasNonLoopback=@($listeners|Where-Object {-not $_.loopback}).Count -gt 0
    $hasIdentityFailure=@($listeners|Where-Object {$_.identity -eq 'FAIL'}).Count -gt 0
    $state=if ($rows.Count -eq 0 -or $hasNonLoopback -or $hasIdentityFailure) {'FAIL'} elseif (-not $ExpectedHash) {'UNKNOWN'} elseif ($ok) {'PASS'} else {'FAIL'}
    return @{status=$state;listeners=$listeners}
}

function Get-G1BillingEstimate([int]$Prompt,[int]$Completion,[string]$Model,[string]$ExpectedModel,[string]$Expression,[decimal]$Rate,[decimal]$Group,[bool]$NoAdditionalChargesVerified,[bool]$RateVerified) {
    if (-not $ExpectedModel -or $Model -cne $ExpectedModel -or $Expression -cne 'tier("base", p * 1 + c * 2)' -or $Rate -le 0 -or $Group -le 0 -or -not $NoAdditionalChargesVerified -or -not $RateVerified) { return @{status='UNKNOWN';model=$Model;prompt_tokens=$Prompt;completion_tokens=$Completion;reason='计价模型、价格表达式、状态接口换算率、组倍率或无附加费用声明未完全核实。'} }
    $usd=([decimal]$Prompt+([decimal]$Completion*2))/1000000; $quota=[decimal]::Round($usd*$Rate*$Group,0,[MidpointRounding]::AwayFromZero)
    return @{status='PASS';model=$Model;prompt_tokens=$Prompt;completion_tokens=$Completion;usd_expression_units=[string]$usd;quota_per_usd=[string]$Rate;group_ratio=[string]$Group;expected_quota=[long]$quota;rounding='half_away_from_zero';expression=$Expression}
}

function Compare-G1Snapshots($Baseline,$Current,[object[]]$BillingEstimates) {
    $assertions=[Collections.Generic.List[object]]::new(); $prior=$Baseline.snapshot
    $requiredSnapshots=@($prior,$Current)
    foreach($snapshot in $requiredSnapshots){
        if($null -eq $snapshot -or $null -eq $snapshot.users -or $null -eq $snapshot.tokens -or $null -eq $snapshot.consume_log_total -or $null -eq $snapshot.consume_logs -or $null -eq $snapshot.mock -or $null -eq $snapshot.mock.counts -or $null -eq $snapshot.mock.chat_responses){
            return @{status='UNKNOWN';mock_inbound_delta=$null;mock_response_delta=$null;consume_log_delta=$null;assertions=@(@{name='snapshot_required_fields';status='UNKNOWN'})}
        }
    }
    if(@($prior.users).Count -eq 0 -or @($Current.users).Count -eq 0 -or @($prior.tokens).Count -eq 0 -or @($Current.tokens).Count -eq 0){
        return @{status='UNKNOWN';mock_inbound_delta=$null;mock_response_delta=$null;consume_log_delta=$null;assertions=@(@{name='user_and_token_baselines_present';status='UNKNOWN'})}
    }
    foreach($snapshot in $requiredSnapshots){
        if($null -eq $snapshot.mock.counts.chat_inbound -or $null -eq $snapshot.mock.counts.chat_response){return @{status='UNKNOWN';mock_inbound_delta=$null;mock_response_delta=$null;consume_log_delta=$null;assertions=@(@{name='mock_count_fields';status='UNKNOWN'})}}
        foreach($user in $snapshot.users){if($null -eq $user.id -or $null -eq $user.quota -or $null -eq $user.used_quota -or $null -eq $user.request_count){return @{status='UNKNOWN';mock_inbound_delta=$null;mock_response_delta=$null;consume_log_delta=$null;assertions=@(@{name='user_baseline_fields';status='UNKNOWN'})}}}
        foreach($token in $snapshot.tokens){if($null -eq $token.id -or $null -eq $token.remain_quota -or $null -eq $token.used_quota){return @{status='UNKNOWN';mock_inbound_delta=$null;mock_response_delta=$null;consume_log_delta=$null;assertions=@(@{name='token_baseline_fields';status='UNKNOWN'})}}}
        foreach($log in $snapshot.consume_logs){if($null -eq $log.id -or $null -eq $log.user_id -or $null -eq $log.token_id -or $null -eq $log.model_name -or $null -eq $log.quota){return @{status='UNKNOWN';mock_inbound_delta=$null;mock_response_delta=$null;consume_log_delta=$null;assertions=@(@{name='consume_log_baseline_fields';status='UNKNOWN'})}}}
        foreach($event in $snapshot.mock.chat_responses){if($null -eq $event.status -or $event.status -eq 200 -and ($null -eq $event.model -or $null -eq $event.prompt_tokens -or $null -eq $event.completion_tokens -or $null -eq $event.total_tokens)){return @{status='UNKNOWN';mock_inbound_delta=$null;mock_response_delta=$null;consume_log_delta=$null;assertions=@(@{name='mock_response_baseline_fields';status='UNKNOWN'})}}}
    }
    $mockInboundDelta=[int]$Current.mock.counts.chat_inbound-[int]$prior.mock.counts.chat_inbound
    $mockResponseDelta=[int]$Current.mock.counts.chat_response-[int]$prior.mock.counts.chat_response
    $logDelta=[int]$Current.consume_log_total-[int]$prior.consume_log_total
    $assertions.Add(@{name='mock_inbound_delta_nonnegative';status=if($mockInboundDelta -ge 0){'PASS'}else{'FAIL'};actual=$mockInboundDelta})
    $assertions.Add(@{name='mock_response_delta_nonnegative';status=if($mockResponseDelta -ge 0){'PASS'}else{'FAIL'};actual=$mockResponseDelta})
    if($mockInboundDelta -lt 0 -or $mockResponseDelta -lt 0 -or $logDelta -lt 0 -or @($Current.mock.chat_responses).Count -lt @($prior.mock.chat_responses).Count){
        $assertions.Add(@{name='snapshot_history_monotonic';status='UNKNOWN'})
        return @{status='UNKNOWN';mock_inbound_delta=$mockInboundDelta;mock_response_delta=$mockResponseDelta;consume_log_delta=$logDelta;assertions=@($assertions)}
    }
    $priorResponses=@($prior.mock.chat_responses); $currentResponses=@($Current.mock.chat_responses)
    $responseHistoryConsistent=($priorResponses.Count -eq [int]$prior.mock.counts.chat_response -and $currentResponses.Count -eq [int]$Current.mock.counts.chat_response -and $currentResponses.Count-$priorResponses.Count -eq $mockResponseDelta)
    $assertions.Add(@{name='mock_response_history_consistent';status=if($responseHistoryConsistent){'PASS'}else{'UNKNOWN'};new_response_count=$currentResponses.Count-$priorResponses.Count;response_count_delta=$mockResponseDelta})
    $assertions.Add(@{name='mock_inbound_response_delta';status=if($mockInboundDelta -eq $mockResponseDelta){'PASS'}else{'UNKNOWN'};inbound_delta=$mockInboundDelta;response_delta=$mockResponseDelta})
    $newResponseEvents=if($responseHistoryConsistent){@($currentResponses|Select-Object -Skip $priorResponses.Count)}else{@()}
    $oldLogIds=@{}; foreach($item in $prior.consume_logs){$key=if($item.request_ref){'ref:'+$item.request_ref}else{'id:'+$item.id};$oldLogIds[$key]=$true}
    $newLogs=@($Current.consume_logs|Where-Object {$key=if($_.request_ref){'ref:'+$_.request_ref}else{'id:'+$_.id};-not $oldLogIds.ContainsKey($key)})
    $assertions.Add(@{name='consume_log_total_delta';status=if($logDelta -eq $newLogs.Count){'PASS'}else{'UNKNOWN'};actual=$logDelta;visible_new_log_count=$newLogs.Count})
    if($null -eq $BillingEstimates -or $newResponseEvents.Count -ne 0 -and $BillingEstimates.Count -eq 0){$assertions.Add(@{name='billing_evidence_present';status='UNKNOWN'})}
    $matchedLogs=[Collections.Generic.List[object]]::new(); $usedLogIds=@{}
    foreach($event in $newResponseEvents){
        if($event.status -ne 200){$assertions.Add(@{name='failed_mock_settlement';status='UNKNOWN';http_status=$event.status});continue}
        if($null -eq $event.prompt_tokens -or $null -eq $event.completion_tokens){$assertions.Add(@{name='mock_usage_available';status='UNKNOWN';model=$event.model});continue}
        $totalOk=([int]$event.total_tokens -eq ([int]$event.prompt_tokens+[int]$event.completion_tokens))
        $assertions.Add(@{name='mock_total_usage_consistent';status=if($totalOk){'PASS'}else{'FAIL'};model=$event.model;total_tokens=$event.total_tokens})
        $match=@($newLogs|Where-Object { -not $usedLogIds.ContainsKey([string]$_.id) -and $_.prompt_tokens -eq $event.prompt_tokens -and $_.completion_tokens -eq $event.completion_tokens -and $_.model_name -eq $event.model })|Select-Object -First 1
        if(-not $match){$assertions.Add(@{name='mock_usage_matches_consume_log';status='FAIL';model=$event.model;prompt_tokens=$event.prompt_tokens;completion_tokens=$event.completion_tokens});continue}
        $log=$match; $usedLogIds[[string]$log.id]=$true; $matchedLogs.Add($log)
        $estimate=@($BillingEstimates|Where-Object {$_.model -eq $event.model -and $_.prompt_tokens -eq $event.prompt_tokens -and $_.completion_tokens -eq $event.completion_tokens}|Select-Object -First 1)
        if($estimate.Count -and $estimate[0].status -eq 'PASS'){
            $expected=[long]$estimate[0].expected_quota
            $assertions.Add(@{name='consume_log_quota_matches_price';status=if([long]$log.quota -eq $expected){'PASS'}else{'FAIL'};log_id=$log.id;expected_quota=$expected;actual_quota=$log.quota})
        } else { $assertions.Add(@{name='consume_log_quota_matches_price';status='UNKNOWN';log_id=$log.id}) }
    }
    if($usedLogIds.Count -ne $newLogs.Count){$assertions.Add(@{name='all_new_consume_logs_matched';status='FAIL';new_log_count=$newLogs.Count;matched_log_count=$usedLogIds.Count})}
    $priorUserIds=@($prior.users|ForEach-Object {[string]$_.id}); $currentUserIds=@($Current.users|ForEach-Object {[string]$_.id})
    $priorTokenIds=@($prior.tokens|ForEach-Object {[string]$_.id}); $currentTokenIds=@($Current.tokens|ForEach-Object {[string]$_.id})
    if(@(Compare-Object $priorUserIds $currentUserIds).Count -or @(Compare-Object $priorTokenIds $currentTokenIds).Count){$assertions.Add(@{name='user_and_token_identity_sets_match';status='UNKNOWN'})}
    foreach($user in $Current.users){
        $before=@($prior.users|Where-Object {[int]$_.id -eq [int]$user.id})|Select-Object -First 1
        if(-not $before){$assertions.Add(@{name='user_baseline_match';status='UNKNOWN';user_id=$user.id});continue}
        $userLogs=@($newLogs|Where-Object {[int]$_.user_id -eq [int]$user.id}); $charge=($userLogs|Measure-Object -Property quota -Sum).Sum; if($null -eq $charge){$charge=0}
        $dq=[int]$user.quota-[int]$before.quota; $du=[int]$user.used_quota-[int]$before.used_quota; $dr=[int]$user.request_count-[int]$before.request_count
        $matches=($dq -eq -[int]$charge -and $du -eq [int]$charge -and $dr -eq $userLogs.Count)
        $assertions.Add(@{name='user_balance_and_request_count_reconciled';status=if($matches){'PASS'}else{'FAIL'};user_id=$user.id;quota_delta=$dq;used_quota_delta=$du;request_count_delta=$dr;new_consume_logs=$userLogs.Count;logged_quota_sum=[int]$charge})
    }
    foreach($token in $Current.tokens){
        $before=@($prior.tokens|Where-Object {[int]$_.id -eq [int]$token.id})|Select-Object -First 1
        if(-not $before){$assertions.Add(@{name='token_baseline_match';status='UNKNOWN';token_id=$token.id});continue}
        if($token.unlimited_quota){$assertions.Add(@{name='key_balance_reconciled';status='UNKNOWN';token_id=$token.id;reason='unlimited quota'});continue}
        $tokenLogs=@($newLogs|Where-Object {[int]$_.token_id -eq [int]$token.id}); $charge=($tokenLogs|Measure-Object -Property quota -Sum).Sum; if($null -eq $charge){$charge=0}
        $dq=[int]$token.remain_quota-[int]$before.remain_quota; $du=[int]$token.used_quota-[int]$before.used_quota
        $matches=($dq -eq -[int]$charge -and $du -eq [int]$charge)
        $assertions.Add(@{name='key_balance_reconciled';status=if($matches){'PASS'}else{'FAIL'};token_id=$token.id;remain_quota_delta=$dq;used_quota_delta=$du;new_consume_logs=$tokenLogs.Count;logged_quota_sum=[int]$charge})
    }
    $state=if(@($assertions|Where-Object {$_.status -eq 'FAIL'}).Count){'FAIL'}elseif(@($assertions|Where-Object {$_.status -eq 'UNKNOWN'}).Count){'UNKNOWN'}else{'PASS'}
    return @{status=$state;mock_inbound_delta=$mockInboundDelta;mock_response_delta=$mockResponseDelta;consume_log_delta=$logDelta;assertions=@($assertions)}
}

function Invoke-G1Acceptance {
    $origin=Assert-G1LoopbackUri $BaseUri
    if ($ExpectedExeSha256 -and $ExpectedExeSha256 -notmatch '^[0-9a-fA-F]{64}$') { throw 'ExpectedExeSha256 must be a 64-character SHA-256.' }
    $status=Invoke-G1LocalGet $origin '/api/status' $null; $listeners=Get-G1ListenerEvidence $Port $ExpectedExeSha256; $apiStatuses=@(@{endpoint='/api/status';http_status=$status.http_status;success=$status.success})
    $checks=[ordered]@{status_http=if($status.http_status -eq 200){'PASS'}else{'FAIL'};source_revision=if($status.data.source_revision -eq $ExpectedRevision){'PASS'}else{'FAIL'};setup=if($status.data.setup -eq $true){'PASS'}else{'FAIL'};registration_closed=if($status.data.register_enabled -eq $false){'PASS'}else{'FAIL'};listener_identity=$listeners.status}
    $users=@(); foreach ($id in $UserIds) { if ($null -eq $AdminBearer) { $checks.user_api='BLOCKED'; break }; $r=Invoke-G1LocalGet $origin "/api/user/$id" $AdminBearer; $apiStatuses+=@(@{endpoint="/api/user/$id";http_status=$r.http_status;success=$r.success}); $users+=,(Get-G1Projection $r.data @('id','username','quota','used_quota','request_count','status','role')); $checks.user_api='PASS' }
    $tokens=@(); foreach ($userKey in $TokenIdsByUser.Keys) { $uid=[string]$userKey; $bearer=$UserBearerById[$uid]; if ($null -eq $bearer) { $checks.token_api='BLOCKED'; continue }; foreach ($tokenId in $TokenIdsByUser[$userKey]) { $r=Invoke-G1LocalGet $origin "/api/token/$tokenId" $bearer; $apiStatuses+=@(@{endpoint="/api/token/$tokenId";http_status=$r.http_status;success=$r.success}); $t=Get-G1Projection $r.data @('id','user_id','status','remain_quota','used_quota','unlimited_quota','model_limits_enabled','model_limits'); if ($t.Contains('user_id') -and [int]$t.user_id -ne [int]$uid) { throw 'Token ownership mismatch.' }; $tokens+=,$t; $checks.token_api='PASS' } }
    $logs=@(); $logTotal=$null
    if ($null -ne $AdminBearer) { $r=Invoke-G1LocalGet $origin '/api/log/?type=2&p=1&page_size=100' $AdminBearer; $apiStatuses+=@(@{endpoint='/api/log/?type=2';http_status=$r.http_status;success=$r.success}); $checks.consume_log_api='PASS'; $logTotal=$r.data.total; foreach ($log in $r.data.items) { $logs+=,(Get-G1LogProjection $log) } }
    $rateVerified=($QuotaPerUsd -gt 0 -and $null -ne $status.data.quota_per_unit -and [decimal]$status.data.quota_per_unit -eq $QuotaPerUsd)
    $mock=Get-G1MockSummary $MockEventsPath; $billing=@(); foreach ($e in $mock.chat_responses) { if ($e.status -eq 200 -and $null -ne $e.prompt_tokens -and $null -ne $e.completion_tokens) { $billing+=,(Get-G1BillingEstimate ([int]$e.prompt_tokens) ([int]$e.completion_tokens) ([string]$e.model) $PricingModel $PricingExpression $QuotaPerUsd $GroupRatio ([bool]$NoAdditionalChargesVerified) $rateVerified) } }
    $snapshot=[ordered]@{users=$users;tokens=$tokens;consume_log_total=$logTotal;consume_logs=$logs;mock=$mock}
    $comparison=@{status='NOT_RUN';note='No baseline supplied.';user_deltas=@();token_deltas=@();consume_log_delta=$null;reconciliation='NOT_RUN'}
    if ($BaselineJsonPath) {
        $base=Get-Content -LiteralPath $BaselineJsonPath -Raw | ConvertFrom-Json -AsHashtable; $ud=@(); $td=@()
        foreach ($u in $users) { $p=@($base.snapshot.users|Where-Object {[int]$_.id -eq [int]$u.id})|Select-Object -First 1; if($p){$ud+=@{id=$u.id;quota=[int]$u.quota-[int]$p.quota;used_quota=[int]$u.used_quota-[int]$p.used_quota;request_count=[int]$u.request_count-[int]$p.request_count}} }
        foreach ($t in $tokens) { $p=@($base.snapshot.tokens|Where-Object {[int]$_.id -eq [int]$t.id})|Select-Object -First 1; if($p){$td+=@{id=$t.id;remain_quota=[int]$t.remain_quota-[int]$p.remain_quota;used_quota=[int]$t.used_quota-[int]$p.used_quota}} }
        $reconciliation=Compare-G1Snapshots $base $snapshot $billing
        $comparison=@{status=$reconciliation.status;user_deltas=$ud;token_deltas=$td;consume_log_delta=if($null -ne $logTotal){[int]$logTotal-[int]$base.snapshot.consume_log_total}else{$null};reconciliation=$reconciliation}
    }
    return [ordered]@{schema='g1-acceptance/v1';generated_at_utc=[DateTime]::UtcNow.ToString('o');target=@{origin=$origin.GetLeftPart([UriPartial]::Authority);expected_revision=$ExpectedRevision};status=@{http_status=$status.http_status;success=$status.success;source_revision=$status.data.source_revision;setup=$status.data.setup;register_enabled=$status.data.register_enabled;quota_per_unit=$status.data.quota_per_unit};api_http_statuses=$apiStatuses;checks=$checks;listener=$listeners;snapshot=$snapshot;comparison=$comparison;billing_estimates=$billing;future_assertions=@{sse='NOT_RUN';failure_refund='NOT_RUN';timeout='NOT_RUN';retry='NOT_RUN';ab_authorization_isolation='NOT_RUN'}}
}

function Get-G1AcceptanceExitCode($Result) {
    $checkStates=@($Result.checks.Values)
    if (@($checkStates|Where-Object {$_ -eq 'FAIL'}).Count -or $Result.comparison.status -eq 'FAIL') { return 2 }
    $requiredChecks=@('status_http','source_revision','setup','registration_closed','listener_identity','user_api','token_api','consume_log_api')
    $missingChecks=@($requiredChecks|Where-Object { $_ -notin $Result.checks.Keys })
    if ($missingChecks.Count -or @($checkStates|Where-Object {$_ -ne 'PASS'}).Count -or $Result.comparison.status -ne 'PASS') { return 3 }
    return 0
}

function Invoke-G1AcceptanceCli($Result,[string]$ReportPath=$OutputPath) {
    try {
        if ($null -eq $Result) { $Result=Invoke-G1Acceptance }
        $json=$Result|ConvertTo-Json -Depth 12
        $fileHash=$null
        if ($ReportPath) {
            Set-Content -LiteralPath $ReportPath -Value $json -Encoding utf8NoBOM -ErrorAction Stop
            $fileHash=(Get-FileHash -LiteralPath $ReportPath -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
        }
        $summary=@('G1 只读验收摘要',"目标：$($Result.target.origin)","源码 SHA：$ExpectedRevision",("检查："+($Result.checks|ConvertTo-Json -Compress)),"用户数：$($Result.snapshot.users.Count)；Key 元数据数：$($Result.snapshot.tokens.Count)；消费日志总数：$($Result.snapshot.consume_log_total)",("Mock："+($Result.snapshot.mock.counts|ConvertTo-Json -Compress)),"账务对账：$($Result.comparison.status)",'SSE、失败退款、超时、重试、A/B 隔离：NOT_RUN') -join [Environment]::NewLine
        Write-Output $summary
        if ($fileHash) { Write-Output ("脱敏 JSON SHA-256：" + $fileHash) }
        $script:G1CliExitCode=Get-G1AcceptanceExitCode $Result
    } catch {
        Write-Error 'G1 acceptance failed; no successful report status is available.' -ErrorAction Continue
        $script:G1CliExitCode=2
    }
}

if ($MyInvocation.InvocationName -ne '.') {
    Invoke-G1AcceptanceCli
    exit $script:G1CliExitCode
}
