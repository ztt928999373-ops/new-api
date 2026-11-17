// Copyright (c) QuantumNous and contributors.
// Package totpops owns the explicit single-instance local TOTP key lifecycle.
package totpops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
)

const ringFile = "totp.ring"
const identityFile = "identity.json"

type KeyIdentity struct {
	Format      uint32                    `json:"format"`
	Environment string                    `json:"environment"`
	Namespace   string                    `json:"namespace"`
	Probe       credentialcrypto.Envelope `json:"probe"`
}

func probeIdentity(m KeyIdentity) credentialcrypto.Identity {
	return credentialcrypto.Identity{Environment: m.Environment, TenantID: m.Namespace, OwnerType: "operator", OwnerID: "offline", CredentialID: m.Namespace, Provider: "totp", CredentialType: "key-probe", Purpose: "recovery-verification", Version: 1}
}
func privateRead(path string) ([]byte, error) {
	if credentialcrypto.ValidatePrivateArtifact(path, false) != nil {
		return nil, model.ErrTOTPStorage
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, model.ErrTOTPStorage
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil || len(b) >= 1<<20 {
		clear(b)
		return nil, model.ErrTOTPStorage
	}
	return b, nil
}
func writeNew(path string, b []byte) error {
	f, err := credentialcrypto.CreatePrivateArtifact(path)
	if err != nil {
		return model.ErrTOTPStorage
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil || closed != nil {
		return model.ErrTOTPStorage
	}
	return nil
}
func InitializeKeys(dir, environment string) error {
	if environment != "test" && environment != "development" && environment != "production" {
		return model.ErrTOTPStorage
	}
	if !filepath.IsAbs(dir) || credentialcrypto.InitializeLocalDirectory(dir) != nil {
		return model.ErrTOTPStorage
	}
	if credentialcrypto.InitializeLocalFile(filepath.Join(dir, ringFile)) != nil {
		return model.ErrTOTPStorage
	}
	p, err := credentialcrypto.NewLocalFileKeyProvider(filepath.Join(dir, ringFile))
	if err != nil {
		return model.ErrTOTPStorage
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return model.ErrTOTPStorage
	}
	m := KeyIdentity{Format: 1, Environment: environment, Namespace: hex.EncodeToString(id)}
	m.Probe, err = credentialcrypto.Seal(context.Background(), p, "totp-local-v1", probeIdentity(m), []byte("totp-key-recovery-v1"))
	if err != nil {
		return model.ErrTOTPStorage
	}
	b, err := common.Marshal(m)
	if err != nil {
		return model.ErrTOTPStorage
	}
	proof, err := os.MkdirTemp(filepath.Dir(dir), ".totp-init-recovery-")
	if err != nil {
		return model.ErrTOTPStorage
	}
	defer os.RemoveAll(proof)
	backup := filepath.Join(proof, "verified.ring")
	if p.SnapshotTo(backup) != nil || writeNew(backup+".identity.json", b) != nil || RestoreKeys(backup, filepath.Join(proof, "restored")) != nil {
		return model.ErrTOTPStorage
	}
	recovered, _, err := OpenKeys(filepath.Join(proof, "restored"))
	if err != nil || recovered.Namespace != m.Namespace || recovered.Environment != m.Environment {
		return model.ErrTOTPStorage
	}
	return writeNew(filepath.Join(dir, identityFile), b)
}
func OpenKeys(dir string) (*model.TOTPSecretStore, KeyIdentity, error) {
	var m KeyIdentity
	if !filepath.IsAbs(dir) || credentialcrypto.ValidatePrivateArtifact(dir, true) != nil {
		return nil, m, model.ErrTOTPStorage
	}
	b, err := privateRead(filepath.Join(dir, identityFile))
	if err != nil {
		return nil, m, err
	}
	defer clear(b)
	if common.Unmarshal(b, &m) != nil || m.Format != 1 || len(m.Namespace) != 32 {
		return nil, m, model.ErrTOTPStorage
	}
	p, err := credentialcrypto.NewLocalFileKeyProvider(filepath.Join(dir, ringFile))
	if err != nil {
		return nil, m, model.ErrTOTPStorage
	}
	data, err := credentialcrypto.Open(context.Background(), p, "totp-local-v1", probeIdentity(m), m.Probe)
	valid := err == nil && string(data) == "totp-key-recovery-v1"
	clear(data)
	if !valid {
		return nil, m, model.ErrTOTPStorage
	}
	return &model.TOTPSecretStore{Environment: m.Environment, Namespace: m.Namespace, Provider: p}, m, nil
}
func Acquire(dir string) (func(), error) {
	return credentialcrypto.AcquireLocalLease(filepath.Join(dir, "instance.lock"))
}
func SnapshotKeys(dir, backup string) error {
	s, m, err := OpenKeys(dir)
	if err != nil {
		return err
	}
	p, ok := s.Provider.(*credentialcrypto.LocalFileKeyProvider)
	if !ok {
		return model.ErrTOTPStorage
	}
	if p.SnapshotTo(backup) != nil {
		return model.ErrTOTPStorage
	}
	b, err := common.Marshal(m)
	if err != nil {
		return model.ErrTOTPStorage
	}
	return writeNew(backup+".identity.json", b)
}

// RestoreKeys always targets a new private directory and rotates BEFORE any new
// encryption. Lost/corrupt keys are never regenerated or repaired automatically.
func RestoreKeys(backup, dir string) error {
	b, err := privateRead(backup + ".identity.json")
	if err != nil {
		return err
	}
	if credentialcrypto.InitializeLocalDirectory(dir) != nil {
		return model.ErrTOTPStorage
	}
	p, err := credentialcrypto.NewLocalFileKeyProvider(backup)
	if err != nil {
		return model.ErrTOTPStorage
	}
	if p.SnapshotTo(filepath.Join(dir, ringFile)) != nil || writeNew(filepath.Join(dir, identityFile), b) != nil {
		return model.ErrTOTPStorage
	}
	s, _, err := OpenKeys(dir)
	if err != nil {
		return err
	}
	if _, err = s.Provider.RotateKEK(context.Background()); err != nil {
		return model.ErrTOTPStorage
	}
	return nil
}
