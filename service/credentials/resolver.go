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
	Identity       credentialcrypto.Identity
	Envelope       credentialcrypto.Envelope
	Status         string
	CurrentVersion uint64
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
	History     HistoricalAuthorizer
}

// WithCredential deliberately has no plaintext return value and no secret cache.
// The synchronous consumer must not retain or log plaintext. Network I/O uses
// WithCredentialContext so the process-local lease can cancel the actual call.
func (r *Resolver) WithCredential(ctx context.Context, actor Principal, id string, version uint64, purpose string, use func([]byte) error) error {
	if use == nil {
		return credentialcrypto.ErrUnavailable
	}
	return r.WithCredentialContext(ctx, actor, id, version, purpose, func(_ context.Context, secret []byte) error { return use(secret) })
}

// WithCredentialContext admits only the requested CURRENT version. The supplied
// context must be used by outbound I/O so local revocation can cancel it.
func (r *Resolver) WithCredentialContext(ctx context.Context, actor Principal, id string, version uint64, purpose string, use func(context.Context, []byte) error) error {
	return r.withLease(ctx, actor, id, version, purpose, "", use)
}

func (r *Resolver) WithHistoricalCredential(ctx context.Context, actor Principal, id string, version uint64, purpose, reference string, use func(context.Context, []byte) error) error {
	if reference == "" || r.History == nil {
		return credentialcrypto.ErrUnavailable
	}
	return r.withLease(ctx, actor, id, version, purpose, reference, use)
}

func (r *Resolver) withLease(ctx context.Context, actor Principal, id string, version uint64, purpose, history string, use func(context.Context, []byte) error) error {
	if use == nil || version == 0 {
		return credentialcrypto.ErrUnavailable
	}
	lease, secret, err := r.admit(ctx, actor, id, version, purpose, history)
	if err != nil {
		return err
	}
	defer lease.Release()
	defer clear(secret)
	if lease.ctx.Err() != nil || use(lease.ctx, secret) != nil || lease.ctx.Err() != nil {
		return credentialcrypto.ErrUnavailable
	}
	return nil
}

// AcquireCurrent copies a credential only into a request-local consumer. The
// caller must bind Lease.Context to the complete request and defer Release.
func (r *Resolver) AcquireCurrent(ctx context.Context, actor Principal, id, purpose string, consume func([]byte) error) (*Lease, error) {
	if consume == nil {
		return nil, credentialcrypto.ErrUnavailable
	}
	lease, secret, err := r.admit(ctx, actor, id, 0, purpose, "")
	if err != nil {
		return nil, err
	}
	defer clear(secret)
	if lease.ctx.Err() != nil || consume(secret) != nil || lease.ctx.Err() != nil {
		lease.Release()
		return nil, credentialcrypto.ErrUnavailable
	}
	return lease, nil
}

func (r *Resolver) admit(ctx context.Context, actor Principal, id string, version uint64, purpose, history string) (*Lease, []byte, error) {
	if r.Repository == nil || r.Audit == nil || ctx.Err() != nil || actor.UserID == "" || actor.TenantID == "" || id == "" {
		return nil, nil, credentialcrypto.ErrUnavailable
	}
	g := credentialAdmission(r.Environment, id)
	g.mu.Lock()
	defer g.mu.Unlock()
	if version == 0 {
		current, ok := r.Repository.(interface {
			CurrentVersion(context.Context, string) (uint64, error)
		})
		if !ok {
			return nil, nil, credentialcrypto.ErrUnavailable
		}
		var err error
		version, err = current.CurrentVersion(ctx, id)
		if err != nil {
			return nil, nil, credentialcrypto.ErrUnavailable
		}
	}
	if version == 0 {
		return nil, nil, credentialcrypto.ErrUnavailable
	}
	record, err := r.Repository.GetForUse(ctx, id, version)
	i := record.Identity
	allowed := err == nil && record.Status == "active" && i.CredentialID == id && i.Version == version && i.Environment == r.Environment && i.TenantID == actor.TenantID && i.Purpose == purpose
	allowed = allowed && ((i.OwnerType == "byok" && i.OwnerID == actor.UserID) || (i.OwnerType == "platform" && actor.PlatformOperator))
	if history == "" {
		allowed = allowed && record.CurrentVersion == version
	} else {
		allowed = allowed && version <= record.CurrentVersion && r.History != nil
		if allowed {
			allowed = r.History.AuthorizeHistorical(ctx, actor, record, history) == nil
		}
	}
	event := Event{CredentialID: id, Version: version, ActorID: actor.UserID, Outcome: "denied"}
	if !allowed {
		_ = r.Audit.Record(ctx, event)
		return nil, nil, credentialcrypto.ErrUnavailable
	}
	provider := r.Providers[record.Envelope.ProviderID]
	plaintext, err := credentialcrypto.Open(ctx, provider, record.Envelope.ProviderID, i, record.Envelope)
	if err != nil {
		event.Outcome = "unavailable"
		_ = r.Audit.Record(ctx, event)
		return nil, nil, credentialcrypto.ErrUnavailable
	}
	event.Outcome = "authorized"
	if history != "" {
		event.Outcome = "authorized_history"
	}
	if r.Audit.Record(ctx, event) != nil || ctx.Err() != nil {
		clear(plaintext)
		return nil, nil, credentialcrypto.ErrUnavailable
	}
	leaseCtx, cancel := context.WithCancel(ctx)
	lease := &Lease{ctx: leaseCtx, cancel: cancel, barrier: g, version: version, historical: history != ""}
	g.active[lease] = struct{}{}
	return lease, plaintext, nil
}
