// Copyright (c) QuantumNous and contributors.
package totpops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openDatabase(path string) (*gorm.DB, func(), error) {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "?#") || credentialcrypto.ValidatePrivateArtifact(filepath.Dir(path), true) != nil || credentialcrypto.ValidatePrivateArtifact(path, false) != nil {
		return nil, nil, model.ErrTOTPStorage
	}
	db, err := gorm.Open(sqlite.Open(path+"?_pragma=busy_timeout(1000)&_txlock=immediate"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, nil, model.ErrTOTPStorage
	}
	sql, err := db.DB()
	if err != nil {
		return nil, nil, model.ErrTOTPStorage
	}
	sql.SetMaxOpenConns(1)
	closeDB := func() { sql.Close() }
	var check string
	if db.Raw("PRAGMA quick_check").Scan(&check).Error != nil || check != "ok" {
		closeDB()
		return nil, nil, model.ErrTOTPStorage
	}
	return db, closeDB, nil
}
func fileHash(path string) (string, error) {
	b, err := os.Open(path)
	if err != nil {
		return "", model.ErrTOTPStorage
	}
	defer b.Close()
	h := sha256.New()
	if _, err = io.Copy(h, b); err != nil {
		return "", model.ErrTOTPStorage
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func copyNew(source, target string) error {
	if credentialcrypto.ValidatePrivateArtifact(source, false) != nil {
		return model.ErrTOTPStorage
	}
	in, err := os.Open(source)
	if err != nil {
		return model.ErrTOTPStorage
	}
	defer in.Close()
	f, err := credentialcrypto.CreatePrivateArtifact(target)
	if err != nil {
		return model.ErrTOTPStorage
	}
	_, err = io.Copy(f, in)
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil || closed != nil {
		return model.ErrTOTPStorage
	}
	return nil
}

type backupManifest struct {
	Format                                                            uint32
	DatabaseSHA256, KeySHA256, IdentitySHA256, Namespace, Environment string
}

func backupLocked(database, keys, dbBackup, keyBackup string, legacy bool) error {
	if !filepath.IsAbs(dbBackup) || !filepath.IsAbs(keyBackup) || filepath.Dir(dbBackup) == filepath.Dir(keyBackup) {
		return model.ErrTOTPStorage
	}
	if credentialcrypto.ValidatePrivateArtifact(filepath.Dir(dbBackup), true) != nil || credentialcrypto.ValidatePrivateArtifact(filepath.Dir(keyBackup), true) != nil {
		return model.ErrTOTPStorage
	}
	s, _, err := OpenKeys(keys)
	if err != nil {
		return err
	}
	db, closeDB, err := openDatabase(database)
	if err != nil {
		return err
	}
	defer closeDB()
	inventory, err := model.InventoryTOTP(db, s)
	if err != nil || inventory.Anomalous != 0 || (!legacy && inventory.Plaintext != 0) {
		return model.ErrTOTPStorage
	}
	if !legacy && model.TOTPReady(db, s) != nil {
		return model.ErrTOTPStorage
	}
	if _, err = os.Lstat(dbBackup); !os.IsNotExist(err) {
		return model.ErrTOTPStorage
	}
	// VACUUM INTO binds the destination; no concatenated SQL path or live-file copy.
	if db.Exec("VACUUM INTO ?", dbBackup).Error != nil {
		return model.ErrTOTPStorage
	}
	if os.Chmod(dbBackup, 0600) != nil || SnapshotKeys(keys, keyBackup) != nil {
		return model.ErrTOTPStorage
	}
	m := backupManifest{Format: 1, Namespace: s.Namespace, Environment: s.Environment}
	m.DatabaseSHA256, err = fileHash(dbBackup)
	if err != nil {
		return err
	}
	m.KeySHA256, err = fileHash(keyBackup)
	if err != nil {
		return err
	}
	m.IdentitySHA256, err = fileHash(keyBackup + ".identity.json")
	if err != nil {
		return err
	}
	b, err := common.Marshal(m)
	if err != nil {
		return model.ErrTOTPStorage
	}
	return writeNew(dbBackup+".totp-manifest.json", b)
}
func restore(databaseBackup, keyBackup, destination, keys string, legacy bool) error {
	b, err := privateRead(databaseBackup + ".totp-manifest.json")
	if err != nil {
		return err
	}
	var m backupManifest
	if common.Unmarshal(b, &m) != nil || m.Format != 1 {
		return model.ErrTOTPStorage
	}
	for path, expected := range map[string]string{databaseBackup: m.DatabaseSHA256, keyBackup: m.KeySHA256, keyBackup + ".identity.json": m.IdentitySHA256} {
		h, e := fileHash(path)
		if e != nil || h != expected {
			return model.ErrTOTPStorage
		}
	}
	if !filepath.IsAbs(destination) || copyNew(databaseBackup, destination) != nil || RestoreKeys(keyBackup, keys) != nil {
		return model.ErrTOTPStorage
	}
	s, _, err := OpenKeys(keys)
	if err != nil || s.Namespace != m.Namespace || s.Environment != m.Environment {
		return model.ErrTOTPStorage
	}
	db, closeDB, err := openDatabase(destination)
	if err != nil {
		return err
	}
	defer closeDB()
	inventory, err := model.InventoryTOTP(db, s)
	if err != nil || inventory.Anomalous != 0 || (!legacy && inventory.Plaintext != 0) {
		return model.ErrTOTPStorage
	}
	if !legacy {
		return model.PublishTOTPReady(db, s)
	}
	return nil
}
func Backup(database, keys, dbBackup, keyBackup string) error {
	release, err := Acquire(keys)
	if err != nil {
		return err
	}
	defer release()
	return backupLocked(database, keys, dbBackup, keyBackup, false)
}
func Restore(databaseBackup, keyBackup, destination, keys string) error {
	return restore(databaseBackup, keyBackup, destination, keys, false)
}

// Migrate never alters the source DB. Offline/exclusive operator control over old
// binaries is a mandatory precondition; their code cannot honor a new lease.
func Migrate(database, destination, keys, dbBackup, keyBackup string) error {
	release, err := Acquire(keys)
	if err != nil {
		return err
	}
	defer release()
	if backupLocked(database, keys, dbBackup, keyBackup, true) != nil {
		return model.ErrTOTPStorage
	}
	proof, err := os.MkdirTemp(filepath.Dir(dbBackup), "totp-preflight-")
	if err != nil {
		return model.ErrTOTPStorage
	}
	defer os.RemoveAll(proof)
	if restore(dbBackup, keyBackup, filepath.Join(proof, "verified.sqlite"), filepath.Join(proof, "keys"), true) != nil {
		return model.ErrTOTPStorage
	}
	if copyNew(dbBackup, destination) != nil {
		return model.ErrTOTPStorage
	}
	s, _, err := OpenKeys(keys)
	if err != nil {
		return err
	}
	db, closeDB, err := openDatabase(destination)
	if err != nil {
		return err
	}
	defer closeDB()
	if db.AutoMigrate(&model.TwoFA{}, &model.TOTPStorageState{}) != nil {
		return model.ErrTOTPStorage
	}
	if model.MigrateTOTP(db, s, nil) != nil {
		return model.ErrTOTPStorage
	}
	// Prove the encrypted staged database + independently backed-up ring recover
	// before publishing READY. The proof restores rotate their active wrapping KEK.
	if backupLocked(destination, keys, dbBackup+".encrypted", keyBackup+".encrypted", true) != nil {
		return model.ErrTOTPStorage
	}
	proof2, err := os.MkdirTemp(filepath.Dir(dbBackup), "totp-recovery-")
	if err != nil {
		return model.ErrTOTPStorage
	}
	defer os.RemoveAll(proof2)
	if restore(dbBackup+".encrypted", keyBackup+".encrypted", filepath.Join(proof2, "verified.sqlite"), filepath.Join(proof2, "keys"), false) != nil {
		return model.ErrTOTPStorage
	}
	return model.PublishTOTPReady(db, s)
}

// Resume reuses the staged destination after an interrupted migration. It never
// re-encrypts known valid rows and repeats the restore proof before READY.
func Resume(database, keys, dbBackup, keyBackup string) error {
	release, err := Acquire(keys)
	if err != nil {
		return err
	}
	defer release()
	s, _, err := OpenKeys(keys)
	if err != nil {
		return err
	}
	db, closeDB, err := openDatabase(database)
	if err != nil {
		return err
	}
	defer closeDB()
	if model.CheckTOTPStage(db, s) != nil {
		return model.ErrTOTPStorage
	}
	if db.AutoMigrate(&model.TwoFA{}, &model.TOTPStorageState{}) != nil || model.MigrateTOTP(db, s, nil) != nil {
		return model.ErrTOTPStorage
	}
	if backupLocked(database, keys, dbBackup, keyBackup, true) != nil {
		return model.ErrTOTPStorage
	}
	proof, err := os.MkdirTemp(filepath.Dir(dbBackup), "totp-recovery-")
	if err != nil {
		return model.ErrTOTPStorage
	}
	defer os.RemoveAll(proof)
	if restore(dbBackup, keyBackup, filepath.Join(proof, "verified.sqlite"), filepath.Join(proof, "keys"), false) != nil {
		return model.ErrTOTPStorage
	}
	return model.PublishTOTPReady(db, s)
}
func InitializeDatabase(database, keys string) error {
	s, _, err := OpenKeys(keys)
	if err != nil {
		return err
	}
	db, closeDB, err := openDatabase(database)
	if err != nil {
		return err
	}
	defer closeDB()
	var n int64
	if db.Unscoped().Model(&model.TwoFA{}).Count(&n).Error != nil || n != 0 {
		return model.ErrTOTPStorage
	}
	if model.MigrateTOTP(db, s, nil) != nil {
		return model.ErrTOTPStorage
	}
	return model.PublishTOTPReady(db, s)
}
func Rotate(database, keys string) error {
	release, err := Acquire(keys)
	if err != nil {
		return err
	}
	defer release()
	s, _, err := OpenKeys(keys)
	if err != nil {
		return err
	}
	db, closeDB, err := openDatabase(database)
	if err != nil {
		return err
	}
	defer closeDB()
	if model.TOTPReady(db, s) != nil {
		return model.ErrTOTPStorage
	}
	if _, err = s.Provider.RotateKEK(context.Background()); err != nil {
		return model.ErrTOTPStorage
	}
	return model.RewrapTOTP(db, s)
}
