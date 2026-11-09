// Copyright (c) QuantumNous and contributors.
// Package credentialcrypto implements versioned supplier credential envelopes.
// It does not authorize callers; authorization belongs to CredentialResolver.
package credentialcrypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
)

var ErrUnavailable = errors.New("credential unavailable")

type Identity struct {
	Environment    string
	TenantID       string
	OwnerType      string
	OwnerID        string
	CredentialID   string
	Provider       string
	CredentialType string
	Purpose        string
	Version        uint64
}

// AAD has a fixed, length-prefixed field order. Identity fields are immutable.
func (i Identity) aad(domain string) ([]byte, error) {
	b := []byte("suanjie-credential-v1")
	for _, v := range []string{domain, i.Environment, i.TenantID, i.OwnerType, i.OwnerID, i.CredentialID, i.Provider, i.CredentialType, i.Purpose} {
		if len(v) == 0 || len(v) > 1024 {
			return nil, ErrUnavailable
		}
		b = binary.BigEndian.AppendUint32(b, uint32(len(v)))
		b = append(b, v...)
	}
	if i.Version == 0 {
		return nil, ErrUnavailable
	}
	return binary.BigEndian.AppendUint64(b, i.Version), nil
}

type WrappedDEK struct {
	KeyVersion string
	Nonce      []byte
	Ciphertext []byte // includes the GCM authentication tag
}

// KeyProvider manages KEKs; database records never contain KEK material.
// Rotation is an operator action, never an inference-request side effect.
type KeyProvider interface {
	EncryptDEK(context.Context, []byte, []byte) (WrappedDEK, error)
	DecryptDEK(context.Context, WrappedDEK, []byte) ([]byte, error)
	RotateKEK(context.Context) (string, error)
	RewrapDEK(context.Context, WrappedDEK, []byte) (WrappedDEK, error)
	GetKeyVersion(context.Context) (string, error)
}

type Envelope struct {
	FormatVersion uint32
	ProviderID    string
	Nonce         []byte
	Ciphertext    []byte // includes the authentication tag; no plaintext JSON field
	WrappedDEK    WrappedDEK
}

func aead(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, ErrUnavailable
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrUnavailable
	}
	return cipher.NewGCM(block)
}

// Seal always generates a fresh DEK and encrypts payload exactly once with it.
// Persistence retries must persist the returned envelope, not reuse a DEK.
func Seal(ctx context.Context, p KeyProvider, providerID string, id Identity, plaintext []byte) (Envelope, error) {
	if ctx.Err() != nil || p == nil || providerID == "" || len(plaintext) == 0 || len(plaintext) > 1<<20 {
		return Envelope{}, ErrUnavailable
	}
	dataAAD, err := id.aad("payload")
	if err != nil {
		return Envelope{}, err
	}
	wrapAAD, _ := id.aad("dek-wrap")
	dek := make([]byte, 32)
	if _, err = rand.Read(dek); err != nil {
		return Envelope{}, ErrUnavailable
	}
	defer clear(dek)
	gcm, err := aead(dek)
	if err != nil {
		return Envelope{}, ErrUnavailable
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return Envelope{}, ErrUnavailable
	}
	wrapped, err := p.EncryptDEK(ctx, dek, wrapAAD)
	if err != nil {
		return Envelope{}, ErrUnavailable
	}
	return Envelope{FormatVersion: 1, ProviderID: providerID, Nonce: nonce, Ciphertext: gcm.Seal(nil, nonce, plaintext, dataAAD), WrappedDEK: wrapped}, nil
}

// Open must only be called after trusted server-side ownership/lifecycle checks.
func Open(ctx context.Context, p KeyProvider, providerID string, id Identity, e Envelope) ([]byte, error) {
	if ctx.Err() != nil || p == nil || providerID == "" || e.FormatVersion != 1 || e.ProviderID != providerID {
		return nil, ErrUnavailable
	}
	dataAAD, err := id.aad("payload")
	if err != nil {
		return nil, err
	}
	wrapAAD, _ := id.aad("dek-wrap")
	dek, err := p.DecryptDEK(ctx, e.WrappedDEK, wrapAAD)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer clear(dek)
	gcm, err := aead(dek)
	if err != nil || len(e.Nonce) != 12 {
		return nil, ErrUnavailable
	}
	data, err := gcm.Open(nil, e.Nonce, e.Ciphertext, dataAAD)
	if err != nil {
		return nil, ErrUnavailable
	}
	return data, nil
}

// Rewrap leaves the content ciphertext and DEK unchanged.
func Rewrap(ctx context.Context, p KeyProvider, id Identity, e Envelope) (Envelope, error) {
	if e.FormatVersion != 1 || p == nil {
		return Envelope{}, ErrUnavailable
	}
	aad, err := id.aad("dek-wrap")
	if err != nil {
		return Envelope{}, err
	}
	w, err := p.RewrapDEK(ctx, e.WrappedDEK, aad)
	if err != nil {
		return Envelope{}, ErrUnavailable
	}
	e.WrappedDEK = w
	return e, nil
}
