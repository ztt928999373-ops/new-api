package middleware

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

const dashboardDenialActorKey = "dashboard_denial_actor"
const dashboardDenialHandledKey = "dashboard_denial_handled"

type denialAuditActor struct {
	ID, Role    int
	AccessToken bool
}

// Per-process fixed window, no attacker-controlled keys or unbounded queue.
// A boundary can admit two windows' budgets. Suppressed/failed events are
// counted in memory and summarized on the next successful write, not replayed.
type denialAuditBudget struct {
	mu         sync.Mutex
	window     time.Time
	writes     int
	suppressed atomic.Uint64
	failed     atomic.Uint64
	inflight   chan struct{}
}

var dashboardDenialBudget = &denialAuditBudget{inflight: make(chan struct{}, 2)}

func recordDashboardDenial(c *gin.Context, status int, code string) {
	if c.GetBool(dashboardDenialHandledKey) {
		return
	}
	c.Set(dashboardDenialHandledKey, true)
	b := dashboardDenialBudget

	// Only fixed reason codes are allowed; never persist raw error messages.
	reason := "permission_denied"
	switch code {
	case "AUTH_UNAUTHORIZED", "AUTH_TOKEN_EXPIRED", "AUTH_SESSION_REVOKED",
		"AUTH_USER_DISABLED", "AUTH_USER_INVALID", "AUTH_INSUFFICIENT_PRIVILEGE",
		"ACCESS_TOKEN_EXPIRED", "ACCESS_TOKEN_LEGACY_RETIRED", "ACCESS_TOKEN_ROUTE_UNDECLARED",
		"ACCESS_TOKEN_SCOPE_DENIED", "AUTH_SESSION_REQUIRED":
		reason = code
	}
	entry := model.AuditLog{
		Category: model.AuditCategorySecurity, Action: "auth.denied", Status: status, Success: false,
		CreatedAt: common.GetTimestamp(), RequestId: c.GetString(common.RequestIdKey),
		Method: c.Request.Method, Route: c.FullPath(), Content: "Dashboard access denied",
		Username: "anonymous", AuthMethod: "unknown",
	}
	if value, exists := c.Get(dashboardDenialActorKey); exists {
		actor := value.(denialAuditActor)
		entry.UserId, entry.ActorRole = actor.ID, actor.Role
		entry.Username = fmt.Sprintf("user-%d", actor.ID)
		entry.AuthMethod = "session"
		if actor.AccessToken {
			entry.AuthMethod = "access_token"
		}
	}
	entry.Other = model.AuditOther{Op: &model.AuditOperation{Action: "auth.denied", Params: model.AuditFields{
		"reason": reason,
	}}}
	// Preserve the existing PAT audit category, non-usable fingerprint and
	// server-selected scope diagnostics without duplicating an unbounded write.
	if value, exists := c.Get(accessTokenAuditContextKey); exists {
		if audit, ok := value.(*accessTokenRequestAudit); ok {
			entry.Category, entry.TokenRef = model.AuditCategoryAccessToken, audit.entry.TokenRef
			entry.UserId, entry.ActorRole = audit.entry.UserId, audit.entry.ActorRole
			entry.Username, entry.AuthMethod = fmt.Sprintf("user-%d", entry.UserId), "access_token"
			if audit.entry.Other.Op != nil {
				for _, key := range []string{"failure_reason", "required_scope"} {
					if v, exists := audit.entry.Other.Op.Params[key]; exists {
						entry.Other.Op.Params[key] = v
					}
				}
			}
		}
	}
	b.record(entry)
}

// Shared bounded writer; each caller owns an independent process-local budget.
// No request-derived map keys, background queue or replay of dropped events.
func (b *denialAuditBudget) record(entry model.AuditLog) {
	select {
	case b.inflight <- struct{}{}:
		defer func() { <-b.inflight }()
	default:
		b.suppressed.Add(1)
		return
	}
	b.mu.Lock()
	now := time.Now()
	if b.window.IsZero() || now.Sub(b.window) >= time.Minute {
		b.window, b.writes = now, 0
	}
	if b.writes >= 60 {
		b.mu.Unlock()
		b.suppressed.Add(1)
		return
	}
	b.writes++
	b.mu.Unlock()
	suppressed, failed := b.suppressed.Swap(0), b.failed.Swap(0)
	entry.Other.Op.Params["suppressed_events"] = suppressed
	entry.Other.Op.Params["failed_events"] = failed
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := model.RecordAuditLogContext(ctx, entry); err != nil {
		b.suppressed.Add(suppressed)
		b.failed.Add(failed + 1)
	}
}

// LoginFailureReasonKey is server-owned metadata, never populated from input.
// Empty marks an accepted login/session or verification challenge.
const LoginFailureReasonKey = "password_login_failure_reason"

// PasswordLoginAudit wraps the real route before global/critical rate limiting,
// body validation and Turnstile so their rejections are covered as well.
func PasswordLoginAudit() gin.HandlerFunc {
	budget := &denialAuditBudget{inflight: make(chan struct{}, 2)}
	return func(c *gin.Context) {
		if c.FullPath() != "/api/user/login" || c.Request.Method != "POST" {
			c.Next()
			return
		}
		c.Set(LoginFailureReasonKey, "request_rejected")
		c.Next()
		reason := c.GetString(LoginFailureReasonKey)
		switch c.Writer.Status() {
		case 429:
			reason = "rate_limited"
		case 400, 413:
			reason = "invalid_parameters"
		case 500, 502, 503, 504:
			reason = "system_failure"
		}
		if reason == "" {
			return
		}
		switch reason {
		case "request_rejected", "rate_limited", "invalid_parameters", "login_disabled", "invalid_credentials", "system_failure":
		default:
			reason = "request_rejected"
		}
		// An unverified account name is neither an actor nor a useful identity
		// claim. Omit username/IP/UA input entirely instead of hashing guessable
		// identities or retaining attacker-controlled strings.
		budget.record(model.AuditLog{
			Category: model.AuditCategoryLogin, Action: "login.failed", Success: false,
			Username: "anonymous", AuthMethod: "password", CreatedAt: common.GetTimestamp(),
			RequestId: c.GetString(common.RequestIdKey), Method: "POST", Route: "/api/user/login",
			Status: c.Writer.Status(), Content: "Login failed",
			Other: model.AuditOther{LoginMethod: "password", Op: &model.AuditOperation{Action: "login.failed", Params: model.AuditFields{"reason": reason}}},
		})
	}
}
