# Redis Authentication Cache Integrity

## Scope and baseline

- Product branch: `product/v1`
- Reproduction baseline: `d6d425a5ad3e0fa8b74d374065063906458e19d1`
- PR #59 and PR #60 are merged. The baseline frontend/backend CI, D2 check, and three-database security regression passed.
- All reproduction data is synthetic. The test used SQLite, miniredis, a synthetic ordinary user, and real Gin HTTP requests through `AdminAuth` and `RootAuth`.

## Reproduction

Before the fix, both protected routes returned `403`. After changing only the cached `Role` in `user:<id>` from ordinary-user role `1` to root role `100`, the same PAT requests reached both handlers:

```text
user_id=1 db_role=1 cached_role=100 admin_http=204 root_http=204 handler_calls=2
```

The authoritative database role remained ordinary-user. This confirms that an actor able to write the Redis user hash could elevate a valid dashboard credential to Admin and Root routes while leaving the database unchanged. The impact is conditional on Redis write access; classify as **P1**, not P0, because the reproduction requires crossing the Redis trust boundary and does not provide unauthenticated network access by itself.

## Candidate mitigation

The candidate authenticates static user identity fields and session snapshots with HMAC. The MAC binds purpose, Redis key, schema, identity/version fields, and cache expiration. `AUTH_CACHE_HMAC_KEY` supplies an optional independent key of at least 32 bytes; if omitted, a purpose-derived key from `CRYPTO_SECRET` is used. Key material is never written to Redis. Old unsigned cache entries miss validation and fall back to the primary database. A database read failure returns an error rather than accepting the cache.

User `Quota` is intentionally excluded from the static identity MAC. Its existing atomic Redis updates, database backfill ordering, and reservation behavior are retained. The focused HTTP test still reserves 25 from a synthetic balance of 100 and observes 75 in Redis. Token authorization continues to read permission fields from the primary database and only merges the pre-existing bounded quota fields from Redis.

## Verification

- `go test ./middleware -run '^TestRedisAuthCacheTamperingCannotElevateDashboardPAT$' -count=1 -v`: PASS. Covers Role and Status tampering over HTTP; Group, Email, Username, Setting, malformed type, expired metadata, cross-key substitution, missing MAC with database failure, session UserID/Status tampering, and atomic user-quota reservation.
- `go test ./middleware -count=1`: PASS.
- `git diff --check`: PASS.
- `go test ./... -run '^$'`: package compilation completed, but test mains for `controller` and `model` could not initialize the repository's Windows private-ACL temporary credential ring (`credential unavailable`). This is an environment limitation; the full root-module test suite is **NOT_RUN** to completion.
- Full root-module race, Windows native runtime, and D3 Full remain **NOT_RUN**.

## Remaining risks and gates

- User wallet `Quota` remains dynamic and is not authenticated by this static-identity MAC; Redis write access can still affect that existing cache value. This candidate does not alter wallet or commercial billing behavior.
- Session tampering that forges a deny marker may force database confirmation and cache misses; it cannot override authoritative session state, but can increase database load.
- Run the candidate's full backend CI and three-database security regression before review. Keep this as a standalone Draft PR; do not merge or deploy automatically.
- TOTP lockout-expiry remediation and G1 acceptance are separate work items and are not included here.
