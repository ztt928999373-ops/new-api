// Copyright (c) QuantumNous and contributors.
package credentials

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/stretchr/testify/require"
)

func TestSecretFieldClassificationRequiresExplicitContext(t *testing.T) {
	require.Equal(t, "restricted_openai_only", CredentialFormatReview("openai", "channel_key_bundle"))
	for _, provider := range []string{"vertex", "codex", "custom", "OpenAI"} {
		require.Equal(t, "pending_confirmation", CredentialFormatReview(provider, "channel_key_bundle"))
	}
	for path, class := range map[string]FieldClassification{
		"channel.key":              SupplierCredential,
		"codex.refresh_token":      OAuthCredential,
		"options.ResendApiKey":     ThirdPartySecret,
		"channel.header_override":  SecretContainer,
		"channel.name":             PublicConfiguration,
		"task.plugin_snapshot.key": PluginIdentifier,
		"custom_json.Key":          PendingClassification,
		"unknown.api_key":          PendingClassification,
		"channel.KEY":              PendingClassification,
	} {
		require.Equal(t, class, ClassifySecretField(path), path)
	}
	copy := SecretFieldInventory()
	copy["channel.key"] = PublicConfiguration
	delete(copy, "codex.refresh_token")
	require.Equal(t, SupplierCredential, ClassifySecretField("channel.key"))
	require.Equal(t, OAuthCredential, ClassifySecretField("codex.refresh_token"))
}

type syntheticStore struct{ record Record }

func (s *syntheticStore) GetForUse(context.Context, string, uint64) (Record, error) {
	return s.record, nil
}

type syntheticAudit struct {
	events []Event
	fail   bool
}

func (a *syntheticAudit) Record(_ context.Context, e Event) error {
	a.events = append(a.events, e)
	if a.fail {
		return errors.New("synthetic audit failure")
	}
	return nil
}

func TestResolverOwnerPlatformRevocationAndAuditFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test-ring")
	require.NoError(t, credentialcrypto.InitializeLocalFile(path))
	p, err := credentialcrypto.NewLocalFileKeyProvider(path)
	require.NoError(t, err)
	i := credentialcrypto.Identity{Environment: "test", TenantID: "default", OwnerType: "byok", OwnerID: "u1", CredentialID: "c1", Provider: "openai", CredentialType: "api_key", Purpose: "relay", Version: 1}
	e, err := credentialcrypto.Seal(ctx, p, "local-test", i, []byte("synthetic-secret"))
	require.NoError(t, err)
	store := &syntheticStore{Record{Identity: i, Envelope: e, Status: "active"}}
	audit := &syntheticAudit{}
	r := Resolver{Environment: "test", Repository: store, Providers: map[string]credentialcrypto.KeyProvider{"local-test": p}, Audit: audit}
	used := 0
	use := func(secret []byte) error { require.Equal(t, "synthetic-secret", string(secret)); used++; return nil }
	owner := Principal{TenantID: "default", UserID: "u1"}
	require.NoError(t, r.WithCredential(ctx, owner, "c1", 1, "relay", use))
	for _, actor := range []Principal{{TenantID: "default", UserID: "u2"}, {TenantID: "default", UserID: "admin", PlatformOperator: true}, {TenantID: "other", UserID: "u1"}} {
		require.ErrorIs(t, r.WithCredential(ctx, actor, "c1", 1, "relay", use), credentialcrypto.ErrUnavailable)
	}
	store.record.Status = "revoked"
	require.ErrorIs(t, r.WithCredential(ctx, owner, "c1", 1, "relay", use), credentialcrypto.ErrUnavailable)
	store.record.Status = "active"
	audit.fail = true
	require.ErrorIs(t, r.WithCredential(ctx, owner, "c1", 1, "relay", use), credentialcrypto.ErrUnavailable)
	audit.fail = false
	i.OwnerType, i.OwnerID = "platform", "platform"
	e, err = credentialcrypto.Seal(ctx, p, "local-test", i, []byte("synthetic-secret"))
	require.NoError(t, err)
	store.record = Record{Identity: i, Envelope: e, Status: "active"}
	require.ErrorIs(t, r.WithCredential(ctx, owner, "c1", 1, "relay", use), credentialcrypto.ErrUnavailable)
	require.NoError(t, r.WithCredential(ctx, Principal{TenantID: "default", UserID: "admin", PlatformOperator: true}, "c1", 1, "relay", use))
	require.Equal(t, 2, used)
}
