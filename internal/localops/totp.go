// Copyright (c) QuantumNous and contributors.
package localops

import (
	"os"
	"path/filepath"

	"github.com/QuantumNous/new-api/internal/totpops"
)

// MigrateTOTPState uses cold verified backups and an entirely new state. Input
// files are never converted in place; an incomplete destination has no READY.
func MigrateTOTPState(source, dbBackup, keyBackup, target string) error {
	if target == "" || Backup(source, dbBackup, keyBackup) != nil || Restore(target, dbBackup, keyBackup) != nil {
		return ErrOperation
	}
	release, err := acquire(target)
	if err != nil {
		return err
	}
	defer release()
	m, err := readManifest(filepath.Join(target, readyName))
	if err != nil {
		return err
	}
	if m.TOTP {
		return nil
	}
	if os.Remove(filepath.Join(target, readyName)) != nil {
		return ErrOperation
	}
	keys := filepath.Join(target, "totp-keys")
	if totpops.InitializeKeys(keys, m.Environment) != nil {
		return ErrOperation
	}
	staged := filepath.Join(target, "totp-staged.sqlite")
	if totpops.Migrate(filepath.Join(target, databaseName), staged, keys, dbBackup+".totp-preflight", keyBackup+".totp-preflight") != nil {
		return ErrOperation
	}
	previous := filepath.Join(target, "pre-totp.sqlite")
	if os.Rename(filepath.Join(target, databaseName), previous) != nil || os.Rename(staged, filepath.Join(target, databaseName)) != nil {
		return ErrOperation
	}
	m.TOTP = true
	if verify(target, m) != nil {
		return ErrOperation
	}
	if os.Remove(previous) != nil {
		return ErrOperation
	}
	return saveManifest(filepath.Join(target, readyName), m)
}
