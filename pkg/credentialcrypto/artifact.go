// Copyright (c) QuantumNous and contributors.
package credentialcrypto

import (
	"github.com/QuantumNous/new-api/common"
	"os"
	"path/filepath"
)

// ValidatePrivateArtifact reuses the provider's platform permission checks.
func ValidatePrivateArtifact(path string, directory bool) error {
	return validatePrivatePath(path, directory)
}

// CreatePrivateArtifact never overwrites an existing file or repairs its ACL.
func CreatePrivateArtifact(path string) (*os.File, error) {
	if validatePrivatePath(filepath.Dir(path), true) != nil {
		return nil, ErrUnavailable
	}
	f, err := createPrivateFile(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	if validatePrivatePath(path, false) != nil {
		f.Close()
		return nil, ErrUnavailable
	}
	return f, nil
}

// SnapshotTo copies a ring under its writer lock to a NEW private backup file.
// A restored snapshot must rotate its active KEK before any new encryption.
func (p *LocalFileKeyProvider) SnapshotTo(path string) error {
	unlock, err := p.lock()
	if err != nil {
		return err
	}
	defer unlock()
	r, err := p.load()
	if err != nil {
		return err
	}
	defer clearRing(r)
	b, err := common.Marshal(r)
	if err != nil {
		return err
	}
	defer clear(b)
	f, err := CreatePrivateArtifact(path)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrUnavailable
	}
	return nil
}
