// Copyright (c) QuantumNous and contributors.
package credentialcrypto

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnvelopeIsolationRotationAndRestore(t *testing.T) {
	ctx := context.Background()
	keyDir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, InitializeLocalDirectory(keyDir))
	path := filepath.Join(keyDir, "synthetic-ring")
	require.NoError(t, InitializeLocalFile(path))
	require.ErrorIs(t, InitializeLocalFile(path), ErrUnavailable)
	p, err := NewLocalFileKeyProvider(path)
	require.NoError(t, err)
	id := Identity{Environment: "test", TenantID: "default", OwnerType: "byok", OwnerID: "user-1", CredentialID: "c-1", Provider: "openai", CredentialType: "api_key", Purpose: "relay", Version: 1}
	e, err := Seal(ctx, p, "local-test", id, []byte("synthetic-secret"))
	require.NoError(t, err)
	plain, err := Open(ctx, p, "local-test", id, e)
	require.NoError(t, err)
	require.Equal(t, "synthetic-secret", string(plain))
	clear(plain)
	for _, mutate := range []func(*Identity){
		func(i *Identity) { i.OwnerID = "user-2" }, func(i *Identity) { i.TenantID = "other" },
		func(i *Identity) { i.OwnerType = "platform" }, func(i *Identity) { i.CredentialID = "c-2" },
		func(i *Identity) { i.Provider = "other" }, func(i *Identity) { i.Purpose = "refresh" },
		func(i *Identity) { i.Version++ }, func(i *Identity) { i.Environment = "prod" },
		func(i *Identity) { i.CredentialType = "oauth" },
	} {
		other := id
		mutate(&other)
		_, err = Open(ctx, p, "local-test", other, e)
		require.ErrorIs(t, err, ErrUnavailable)
	}
	tampered := e
	tampered.Ciphertext = append([]byte(nil), e.Ciphertext...)
	tampered.Ciphertext[0] ^= 1
	_, err = Open(ctx, p, "local-test", id, tampered)
	require.ErrorIs(t, err, ErrUnavailable)
	old := e.WrappedDEK.KeyVersion
	next, err := p.RotateKEK(ctx)
	require.NoError(t, err)
	require.NotEqual(t, old, next)
	plain, err = Open(ctx, p, "local-test", id, e)
	require.NoError(t, err)
	clear(plain)
	rewrapped, err := Rewrap(ctx, p, id, e)
	require.NoError(t, err)
	require.Equal(t, e.Ciphertext, rewrapped.Ciphertext)
	require.Equal(t, next, rewrapped.WrappedDEK.KeyVersion)
	// Synthetic restore is into an isolated path, decryption only. Real restore
	// must rotate before any new wrapping to prevent counter rollback reuse.
	backup, err := os.ReadFile(path)
	require.NoError(t, err)
	restoreDir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, InitializeLocalDirectory(restoreDir))
	restoredPath := filepath.Join(restoreDir, "restored-ring")
	require.NoError(t, InitializeLocalFile(restoredPath))
	require.NoError(t, os.WriteFile(restoredPath, backup, 0600))
	clear(backup)
	restored, err := NewLocalFileKeyProvider(restoredPath)
	require.NoError(t, err)
	plain, err = Open(ctx, restored, "local-test", id, rewrapped)
	require.NoError(t, err)
	require.Equal(t, "synthetic-secret", string(plain))
	clear(plain)
	_, err = restored.RotateKEK(ctx)
	require.NoError(t, err)
	_, err = Open(ctx, restored, "wrong-provider", id, e)
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestLocalNonceReservationPersistsAndFailsClosed(t *testing.T) {
	keyDir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, InitializeLocalDirectory(keyDir))
	path := filepath.Join(keyDir, "ring")
	require.NoError(t, InitializeLocalFile(path))
	p, err := NewLocalFileKeyProvider(path)
	require.NoError(t, err)
	dek := make([]byte, 32) // synthetic fixture only
	a, err := p.EncryptDEK(context.Background(), dek, []byte("aad"))
	require.NoError(t, err)
	p, err = NewLocalFileKeyProvider(path)
	require.NoError(t, err)
	b, err := p.EncryptDEK(context.Background(), dek, []byte("aad"))
	require.NoError(t, err)
	require.NotEqual(t, a.Nonce, b.Nonce)
	wrong := a
	wrong.Nonce = nil
	_, err = p.DecryptDEK(context.Background(), wrong, []byte("aad"))
	require.ErrorIs(t, err, ErrUnavailable)
	wrong = a
	wrong.KeyVersion = "unknown"
	_, err = p.DecryptDEK(context.Background(), wrong, []byte("aad"))
	require.ErrorIs(t, err, ErrUnavailable)
	_, err = p.DecryptDEK(context.Background(), a, []byte("wrong-aad"))
	require.ErrorIs(t, err, ErrUnavailable)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.EncryptDEK(canceled, dek, []byte("aad"))
	require.ErrorIs(t, err, ErrUnavailable)
	require.NoError(t, os.WriteFile(path+".lock", nil, 0600))
	_, err = p.EncryptDEK(context.Background(), dek, []byte("aad"))
	require.ErrorIs(t, err, ErrUnavailable)
	require.NoError(t, os.Remove(path+".lock"))
	require.NoError(t, os.WriteFile(path, []byte("corrupt"), 0600))
	_, err = p.DecryptDEK(context.Background(), a, []byte("aad"))
	require.ErrorIs(t, err, ErrUnavailable)
}
