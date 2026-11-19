# TOTP Lockout Expiry

## Baseline and reproduction

- Baseline: `product/v1` at `d6d425a5ad3e0fa8b74d374065063906458e19d1`, after PR #59 encrypted TOTP storage and PR #60 dependency update.
- Synthetic SQLite row started with `failed_attempts=5` and an expired `locked_until`.
- Before the change, one additional failed-attempt update produced `failed_attempts=6` and a new active lock. The request was therefore immediately locked again after the prior lockout expired.

## Change

`IncrementFailedAttempts` now treats an expired lock as the start of a new failure window: the next failure writes count `1` and clears the old deadline unless that fresh count itself reaches the configured threshold. The existing compare-and-update predicate still uses the stored count and lock deadline, so concurrent requests retry from the winner's new state. Active lock requests remain no-ops. No TOTP encryption, backup-code storage, login flow, or lock duration was changed.

TOTP, backup-code, and step-up failure paths share `IncrementFailedAttempts`; successful TOTP and backup-code paths continue to call the existing reset behavior. Login verification continues to consult the same lock state.

## Verification

- `go test ./middleware -run '^TestExpiredTOTPFailureWindowRestartsAfterLockout$' -count=10 -v`: PASS. Covers active-lock no-op, post-expiry reset to 1, concurrent failures, threshold relock, and no lock extension during active lock.
- `go test ./middleware -count=1`: PASS.
- `go vet ./middleware`: PASS.
- `go build ./...`: PASS (sandboxed first attempt could not update Go module metadata; approved rerun succeeded).
- Targeted `-race`: **NOT_RUN** because this Windows Go environment has `CGO_ENABLED=0` and no C compiler available.
- Local model package test: **NOT_RUN locally**; its test main could not initialize the private credential-ring directory under this Windows environment's ACL behavior. This is a local-environment limitation, not a CI failure.

## Current candidate verification

- Candidate: `e3b8ec6f02a4d92a200b0deeb798aee8c4edf6fc` (PR #62 head); base `product/v1` at `39f35ca1a641d55d1c4ddca4b2b27a15d93d1167`.
- Backend vet, build, and test: **PASS** on the candidate SHA: [Actions run 44576337260](https://github.com/ztt928999373-ops/new-api/actions/runs/44576337260).
- Frontend typecheck and test: **PASS** on the candidate SHA: [Actions run 44576337260](https://github.com/ztt928999373-ops/new-api/actions/runs/44576337260).
- Three-database security regression: **PASS** on the candidate SHA; SQLite, MySQL, and PostgreSQL completed, with 110 reported passing test counts: [Actions run 44576337349](https://github.com/ztt928999373-ops/new-api/actions/runs/44576337349). The `v1-security-regression` artifact was uploaded by this run.
- Go vulnerability scans (`.`, `relaykit`, and `tokenkit`): **PASS**: [Actions run 44576337481](https://github.com/ztt928999373-ops/new-api/actions/runs/44576337481).
- The three-database artifact records passing TOTP encrypted-storage coverage for SQLite, MySQL, and PostgreSQL, including cross-user/cross-record isolation and tamper, missing-key, wrong-key, and envelope-version cases. It also records passing concurrent failed-attempt counting, single-use backup-code, step-up, Session revocation fence, rollback, and Redis-unavailable fail-closed cases.
- **SKIPPED**: 6 budget checks, F1 synthetic security, F1.1 denial-audit, local recovery/race acceptance, and PR #5 credential acceptance. Their workflow conditions target other named branch refs or PR #5, so they are inapplicable to PR #62; the dedicated CI and three-database security regression above ran and passed.
- **NOT_RUN**: complete root-module race, Windows native security run, and D3 Full. The targeted local `-race` remains **NOT_RUN** for the Windows toolchain limitation recorded above. These results are not represented as passing.

No production user records were changed. This is independent from the Redis cache fix and remains a separate Draft PR candidate.
