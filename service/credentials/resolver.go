// Copyright (c) QuantumNous and contributors.
// Package credentials is the application boundary for supplier secret use.
// HTTP adapters must construct Principal from authenticated server state.
package credentials

import (
	"context"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
)

type Principal struct {
	TenantID         string
	UserID           string
	PlatformOperator bool // never authorizes access to another user's BYOK
}

type Record struct {
	Identity credentialcrypto.Identity
	Envelope credentialcrypto.Envelope
	Status   string
}

// Repository must read authoritative status on every use, including historical
// versions. It must never silently serve stale records after revocation.
type Repository interface {
	GetForUse(context.Context, string, uint64) (Record, error)
}

type Event struct {
	CredentialID string
	Version      uint64
	ActorID      string
	Outcome      string
}

type Audit interface {
	Record(context.Context, Event) error
}

type Resolver struct {
	Environment string
	Repository  Repository
	Providers   map[string]credentialcrypto.KeyProvider
	Audit       Audit
}

// WithCredential deliberately has no plaintext return value and no secret cache.
// The consumer must not retain or log plaintext. In-flight upstream operations
// require a separate cancellation registry when production wiring is added.
func (r *Resolver) WithCredential(ctx context.Context, actor Principal, id string, version uint64, purpose string, use func([]byte) error) error {
	if r.Repository == nil || r.Audit == nil || use == nil || ctx.Err() != nil || actor.UserID == "" || actor.TenantID == "" {
		return credentialcrypto.ErrUnavailable
	}
	record, err := r.Repository.GetForUse(ctx, id, version)
	i := record.Identity
	allowed := err == nil && record.Status == "active" && i.CredentialID == id && i.Version == version && i.Environment == r.Environment && i.TenantID == actor.TenantID && i.Purpose == purpose
	allowed = allowed && ((i.OwnerType == "byok" && i.OwnerID == actor.UserID) || (i.OwnerType == "platform" && actor.PlatformOperator))
	event := Event{CredentialID: id, Version: version, ActorID: actor.UserID, Outcome: "denied"}
	if !allowed {
		_ = r.Audit.Record(ctx, event)
		return credentialcrypto.ErrUnavailable
	}
	provider := r.Providers[record.Envelope.ProviderID]
	plaintext, err := credentialcrypto.Open(ctx, provider, record.Envelope.ProviderID, i, record.Envelope)
	if err != nil {
		event.Outcome = "unavailable"
		_ = r.Audit.Record(ctx, event)
		return credentialcrypto.ErrUnavailable
	}
	defer clear(plaintext)
	event.Outcome = "authorized"
	if r.Audit.Record(ctx, event) != nil {
		return credentialcrypto.ErrUnavailable
	}
	if ctx.Err() != nil {
		return credentialcrypto.ErrUnavailable
	}
	if use(plaintext) != nil {
		return credentialcrypto.ErrUnavailable
	}
	return nil
}
