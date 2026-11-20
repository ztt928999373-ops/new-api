# TOTP Lockout Expiry

## Scope

After PR #59 encrypted TOTP storage was integrated, a failed attempt against an expired lock continued the old failure count and could immediately lock the account again. The production fix in this PR resets the count to 1 after expiry, leaves active locks unchanged, and retains the compare-and-update (CAS) retry behavior. This change does not modify Redis authentication caching, TOTP encryption, backup-code storage, login policy, or lock duration.

TOTP, backup-code, and step-up verification continue to use the shared failure-counter behavior. A counter storage error is returned as an authentication error; it must not be converted to a successful verification.

## Historical Evidence

The following records belong to earlier candidates and are retained as history only. They do not establish acceptance of a later PR HEAD.

- Baseline reproduction used synthetic SQLite state with failed_attempts=5 and an expired lock. Before the production fix, another failure produced count 6 and immediately re-locked the account.
- Candidate e3b8ec6f02a4d92a200b0deeb798aee8c4edf6fc, Base 39f35ca1a641d55d1c4ddca4b2b27a15d93d1167: backend and frontend CI PASS at [run 44576337260](https://github.com/ztt928999373-ops/new-api/actions/runs/44576337260); three-database regression PASS at [run 44576337349](https://github.com/ztt928999373-ops/new-api/actions/runs/44576337349); Go vulnerability scans PASS at [run 44576337481](https://github.com/ztt928999373-ops/new-api/actions/runs/44576337481). The earlier report described 110 passing test counts; this is a reported historical count, not a count of unique test cases.
- Candidate e565d1c658fc3a9aeb750b6649d6c250ae46624d, Base 39f35ca1a641d55d1c4ddca4b2b27a15d93d1167: backend/frontend CI PASS at [run 44629454357](https://github.com/ztt928999373-ops/new-api/actions/runs/44629454357); vulnerability scans PASS at [run 44629454189](https://github.com/ztt928999373-ops/new-api/actions/runs/44629454189); three-database security regression and its artifact PASS at [run 44659802282](https://github.com/ztt928999373-ops/new-api/actions/runs/44659802282); manager check PASS at [run 44659802925](https://github.com/ztt928999373-ops/new-api/actions/runs/44659802925).
- The artifact for the preceding e565d1c run contained TOTP encrypted-storage, authentication, backup-code, step-up, and recovery coverage for its configured suites. Those results are historical and are not the evidence for a later code revision.
- Earlier local Windows records: middleware test, vet, and build were reported PASS. Targeted race and local model-package test were NOT_RUN because the environment had CGO_ENABLED=0 and a Windows credential-ring ACL limitation. These environment-specific results are historical.

## Concurrency Test Design

TestIncrementFailedAttemptsMultiConnectionDatabaseMatrix uses isolated SQLite, MySQL, and PostgreSQL databases. Its SQL pool allows up to 16 open and idle connections. A query barrier makes concurrent goroutines read the same expired lock state before their CAS updates proceed, then the test checks that stale updates conflict and retries preserve the expected final count, including concurrent relock at the failure threshold.

The matrix covers expiry reset, concurrent CAS conflict/retry and count consistency, threshold re-lock, concurrent calls during an active lock, and deterministic external updates that force all five CAS attempts to lose. The last case must return the bounded-conflict error rather than report an update as successful. Middleware tests separately retain the SQLite single-connection functional checks and verify that a counter write error propagates through service.VerifyTwoFactorCode.

The dedicated security workflow runs the middleware expiry regression and counter-error test with -race -count=10, and runs the SQLite/MySQL/PostgreSQL multi-connection matrix with -race -count=10. Raw JSONL logs and the environment record are uploaded in v1-security-regression; the run summary must be used to distinguish pass, failure, skip, and not-run outcomes. Repetition counts and parent/subtest events are not unique test-case counts.

## Acceptance Record

Per-revision acceptance is recorded against the exact PR HEAD in the PR checks and final audit report, including Actions run and artifact links. This document intentionally does not name its own commit as a candidate SHA. PASS, FAILED, SKIPPED, and NOT_RUN are separate outcomes; a skipped or unrun test is never represented as passing.

The complete root-module race, Windows native security run, and D3 Full remain separate acceptance items. D3 Full previously passed on historical base 7dc9da0372b7dbd3ebc6a273346523219680ff8e; it is not evidence for a later candidate. No production database, user credentials, or production keys are used by these tests.
