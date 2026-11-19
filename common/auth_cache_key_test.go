package common

import (
	"bytes"
	"testing"
)

func TestAuthCacheIntegrityKeySupportsDedicatedAndDerivedKeys(t *testing.T) {
	oldKey, oldCryptoSecret := AuthCacheHMACKey, CryptoSecret
	t.Cleanup(func() {
		AuthCacheHMACKey, CryptoSecret = oldKey, oldCryptoSecret
	})

	dedicated := []byte("0123456789abcdef0123456789abcdef")
	AuthCacheHMACKey = dedicated
	if !bytes.Equal(AuthCacheIntegrityKey(), dedicated) {
		t.Fatal("dedicated authentication-cache key was not selected")
	}

	AuthCacheHMACKey = nil
	CryptoSecret = "synthetic-crypto-secret"
	want := []byte(GenerateHMACWithKey([]byte(CryptoSecret), "new-api/auth-cache-integrity-key/v1"))
	got := AuthCacheIntegrityKey()
	if !bytes.Equal(got, want) {
		t.Fatal("fallback authentication-cache key was not purpose-derived")
	}
	if bytes.Equal(got, []byte(CryptoSecret)) {
		t.Fatal("fallback authentication-cache key reused the source secret directly")
	}
}
