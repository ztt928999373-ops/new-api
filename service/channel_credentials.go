// Copyright (c) QuantumNous and contributors.
package service

import (
	"context"
	"strconv"
	"sync/atomic"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/QuantumNous/new-api/service/credentials"
)

// Credential wiring is explicit; startup does not migrate data or create KEKs.
var channelCredentialResolver atomic.Pointer[credentials.Resolver]

func SetChannelCredentialResolver(r *credentials.Resolver) *credentials.Resolver {
	return channelCredentialResolver.Swap(r)
}

// ResolveChannelForRequest must follow ordinary channel routing authorization.
// Returned plaintext is request-local, never written to shared channel caches.
func ResolveChannelForRequest(ctx context.Context, ch *model.Channel, userID int) (*model.Channel, error) {
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
