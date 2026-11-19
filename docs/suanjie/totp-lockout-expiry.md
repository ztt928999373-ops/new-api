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
- Model package tests, three-database auth regression, and complete backend CI remain pending; the model test main is blocked locally by the Windows private-ACL credential-ring initialization noted in the Redis report.
- Full root-module race, Windows native security run, and D3 Full remain **NOT_RUN**.

No production user records were changed. This is independent from the Redis cache fix and remains a separate Draft PR candidate.
