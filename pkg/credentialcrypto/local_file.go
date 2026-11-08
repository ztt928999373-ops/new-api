// Copyright (c) QuantumNous and contributors.
package credentialcrypto

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/QuantumNous/new-api/common"
)

type localKey struct {
	Key     []byte
	Counter uint64
}
type localRing struct {
	Format uint32
	Active string
	Keys   map[string]localKey
}

// LocalFileKeyProvider supports a single host and canonical keyring path.
// Every mutation locks, reloads and durably reserves its nonce before encryption.
// Never clone a writable ring or restore counters and resume the old active KEK.
type LocalFileKeyProvider struct{ path string }

// InitializeLocalDirectory explicitly creates a NEW private key directory.
// Startup never repairs an existing directory or broadens/changes its ACL.
func InitializeLocalDirectory(path string) error {
	return createPrivateDirectory(path)
}

func newLocalKey() (string, localKey, error) {
	k := make([]byte, 32)
	id := make([]byte, 16)
	if _, err := rand.Read(k); err != nil {
		return "", localKey{}, ErrUnavailable
	}
	if _, err := rand.Read(id); err != nil {
		clear(k)
		return "", localKey{}, ErrUnavailable
	}
	return hex.EncodeToString(id), localKey{Key: k}, nil
}

// InitializeLocalFile is an explicit operator operation. It never overwrites.
func InitializeLocalFile(path string) error {
	if validatePrivatePath(filepath.Dir(path), true) != nil {
		return ErrUnavailable
	}
	id, key, err := newLocalKey()
	if err != nil {
		return err
	}
	defer clear(key.Key)
	b, err := common.Marshal(localRing{Format: 1, Active: id, Keys: map[string]localKey{id: key}})
	if err != nil {
		return ErrUnavailable
	}
	defer clear(b)
	f, err := createPrivateFile(path)
	if err != nil {
		return ErrUnavailable
	}
	defer f.Close()
	if validatePrivatePath(path, false) != nil {
		return ErrUnavailable
	}
	if _, err = f.Write(b); err != nil {
		return ErrUnavailable
	}
	if err = f.Sync(); err != nil {
		return ErrUnavailable
	}
	return nil
}

func NewLocalFileKeyProvider(path string) (*LocalFileKeyProvider, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	p := &LocalFileKeyProvider{path: abs}
	r, err := p.load()
	if err != nil {
		return nil, err
	}
	defer clearRing(r)
	return p, nil
}

func clearRing(r localRing) {
	for _, k := range r.Keys {
		clear(k.Key)
	}
}

func (p *LocalFileKeyProvider) load() (localRing, error) {
	if validatePrivatePath(filepath.Dir(p.path), true) != nil || validatePrivatePath(p.path, false) != nil {
		return localRing{}, ErrUnavailable
	}
	info, err := os.Lstat(p.path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return localRing{}, ErrUnavailable
	}
	b, err := os.ReadFile(p.path)
	if err != nil {
		return localRing{}, ErrUnavailable
	}
	defer clear(b)
	var r localRing
	if common.Unmarshal(b, &r) != nil || r.Format != 1 || r.Active == "" {
		clearRing(r)
		return localRing{}, ErrUnavailable
	}
	if _, ok := r.Keys[r.Active]; !ok {
		clearRing(r)
		return localRing{}, ErrUnavailable
	}
	for _, k := range r.Keys {
		if len(k.Key) != 32 {
			clearRing(r)
			return localRing{}, ErrUnavailable
		}
	}
	return r, nil
}

func (p *LocalFileKeyProvider) save(r localRing) error {
	if validatePrivatePath(filepath.Dir(p.path), true) != nil || validatePrivatePath(p.path, false) != nil {
		return ErrUnavailable
	}
	b, err := common.Marshal(r)
	if err != nil {
		return ErrUnavailable
	}
	defer clear(b)
	f, err := os.CreateTemp(filepath.Dir(p.path), ".credential-ring-*")
	if err != nil {
		return ErrUnavailable
	}
	name := f.Name()
	defer os.Remove(name)
	if preservePrivatePermissions(name, p.path) != nil || validatePrivatePath(name, false) != nil {
		_ = f.Close()
		return ErrUnavailable
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrUnavailable
	}
	return durableReplace(name, p.path)
}

func (p *LocalFileKeyProvider) lock() (func(), error) {
	if validatePrivatePath(filepath.Dir(p.path), true) != nil {
		return nil, ErrUnavailable
	}
	f, err := createPrivateFile(p.path + ".lock")
	if err != nil {
		return nil, ErrUnavailable
	}
	f.Close()
	return func() { _ = os.Remove(p.path + ".lock") }, nil
}

func (p *LocalFileKeyProvider) GetKeyVersion(ctx context.Context) (string, error) {
	if ctx.Err() != nil {
		return "", ErrUnavailable
	}
	r, err := p.load()
	defer clearRing(r)
	if err != nil {
		return "", err
	}
	return r.Active, nil
}

func (p *LocalFileKeyProvider) EncryptDEK(ctx context.Context, dek, aad []byte) (WrappedDEK, error) {
	if ctx.Err() != nil || len(dek) != 32 || len(aad) == 0 {
		return WrappedDEK{}, ErrUnavailable
	}
	unlock, err := p.lock()
	if err != nil {
		return WrappedDEK{}, err
	}
	defer unlock()
	r, err := p.load()
	if err != nil {
		return WrappedDEK{}, err
	}
	defer clearRing(r)
	k := r.Keys[r.Active]
	if k.Counter >= 1<<32 {
		return WrappedDEK{}, ErrUnavailable
	}
	k.Counter++
	r.Keys[r.Active] = k
	if p.save(r) != nil {
		return WrappedDEK{}, ErrUnavailable
	}
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[4:], k.Counter)
	gcm, err := aead(k.Key)
	if err != nil {
		return WrappedDEK{}, ErrUnavailable
	}
	return WrappedDEK{KeyVersion: r.Active, Nonce: nonce, Ciphertext: gcm.Seal(nil, nonce, dek, aad)}, nil
}

func (p *LocalFileKeyProvider) DecryptDEK(ctx context.Context, w WrappedDEK, aad []byte) ([]byte, error) {
	if ctx.Err() != nil || len(w.Nonce) != 12 || len(aad) == 0 {
		return nil, ErrUnavailable
	}
	r, err := p.load()
	if err != nil {
		return nil, err
	}
	defer clearRing(r)
	k, ok := r.Keys[w.KeyVersion]
	if !ok {
		return nil, ErrUnavailable
	}
	gcm, err := aead(k.Key)
	if err != nil {
		return nil, ErrUnavailable
	}
	dek, err := gcm.Open(nil, w.Nonce, w.Ciphertext, aad)
	if err != nil || len(dek) != 32 {
		clear(dek)
		return nil, ErrUnavailable
	}
	return dek, nil
}

func (p *LocalFileKeyProvider) RotateKEK(ctx context.Context) (string, error) {
	if ctx.Err() != nil {
		return "", ErrUnavailable
	}
	unlock, err := p.lock()
	if err != nil {
		return "", err
	}
	defer unlock()
	r, err := p.load()
	if err != nil {
		return "", err
	}
	defer clearRing(r)
	id, k, err := newLocalKey()
	if err != nil {
		return "", err
	}
	r.Keys[id], r.Active = k, id
	if p.save(r) != nil {
		return "", ErrUnavailable
	}
	return id, nil
}

func (p *LocalFileKeyProvider) RewrapDEK(ctx context.Context, w WrappedDEK, aad []byte) (WrappedDEK, error) {
	dek, err := p.DecryptDEK(ctx, w, aad)
	if err != nil {
		return WrappedDEK{}, err
	}
	defer clear(dek)
	return p.EncryptDEK(ctx, dek, aad)
}
