// Copyright (c) QuantumNous and contributors.
package service

import (
	"context"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/QuantumNous/new-api/service/credentials"
	"gorm.io/gorm"
)

// Credential wiring is explicit; startup does not migrate data or create KEKs.
var channelCredentialResolver atomic.Pointer[credentials.Resolver]

// Configuration is opt-in; this never creates KEKs or migrates legacy secrets.
func InitSupplierCredentialStorage(db *gorm.DB) error {
	path := os.Getenv("SUPPLIER_CREDENTIAL_KEYRING_PATH")
	if path == "" {
		return nil
	}
	environment := os.Getenv("SUPPLIER_CREDENTIAL_ENVIRONMENT")
	if environment != "development" && environment != "test" && environment != "production" {
		return credentialcrypto.ErrUnavailable
	}
	p, err := credentialcrypto.NewLocalFileKeyProvider(path)
	if err != nil {
		return credentialcrypto.ErrUnavailable
	}
	id := "local-" + environment
	s := &credentials.Store{DB: db, Environment: environment, ProviderID: id, Provider: p}
	if s.MigrateSchema() != nil {
		return credentialcrypto.ErrUnavailable
	}
	SetChannelCredentialResolver(&credentials.Resolver{Environment: environment, Repository: s, Providers: map[string]credentialcrypto.KeyProvider{id: p}, Audit: s})
	return nil
}

func SetChannelCredentialResolver(r *credentials.Resolver) *credentials.Resolver {
	return channelCredentialResolver.Swap(r)
}

func ConfiguredCredentialStore() *credentials.Store {
	r := channelCredentialResolver.Load()
	if r == nil {
		return nil
	}
	s, _ := r.Repository.(*credentials.Store)
	return s
}

func WithConfiguredCredential(ctx context.Context, actor credentials.Principal, id string, version uint64, use func([]byte) error) error {
	r := channelCredentialResolver.Load()
	if r == nil {
		return credentialcrypto.ErrUnavailable
	}
	return r.WithCredential(ctx, actor, id, version, "relay", use)
}

// ResolveChannelForRequest must follow ordinary channel routing authorization.
// Returned plaintext is request-local, never written to shared channel caches.
func ResolveChannelForRequest(ctx context.Context, ch *model.Channel, userID int) (*model.Channel, error) {
	if ch == nil {
		return nil, credentialcrypto.ErrUnavailable
	}
	if ch.CredentialRef == "" {
		return ch, nil
	}
	r := channelCredentialResolver.Load()
	if r == nil || userID <= 0 {
		return nil, credentialcrypto.ErrUnavailable
	}
	// Version lookup is authoritative and must not come from channel cache.
	current, ok := r.Repository.(interface {
		CurrentVersion(context.Context, string) (uint64, error)
	})
	if !ok {
		return nil, credentialcrypto.ErrUnavailable
	}
	version, err := current.CurrentVersion(ctx, ch.CredentialRef)
	if err != nil {
		return nil, credentialcrypto.ErrUnavailable
	}
	copy := *ch
	copy.Key = ""
	copy.Keys = nil
	err = r.WithCredential(ctx, credentials.Principal{TenantID: "default", UserID: strconv.Itoa(userID), PlatformOperator: true}, ch.CredentialRef, version, "relay", func(b []byte) error { copy.Key = string(b); return nil })
	if err != nil {
		return nil, credentialcrypto.ErrUnavailable
	}
	return &copy, nil
}
