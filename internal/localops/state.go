// Copyright (c) QuantumNous and contributors.
// Package localops implements an opt-in, single-instance SQLite operations profile.
// It never reads dotenv, accepts database DSNs, or overwrites a state directory.
package localops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/QuantumNous/new-api/service/credentials"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var ErrOperation = errors.New("local operation unavailable: check state, permissions, backup pair and exclusive access")

const databaseName = "database.sqlite"
const ringName = "supplier.ring"
const readyName = "ready.json"

type Manifest struct {
	Build          string                    `json:"build"`
	Format         int                       `json:"format"`
	Environment    string                    `json:"environment"`
	ID             string                    `json:"id"`
	Probe          credentialcrypto.Envelope `json:"probe"`
	DatabaseSHA256 string                    `json:"database_sha256,omitempty"`
	KeySHA256      string                    `json:"key_sha256,omitempty"`
}

func probeIdentity(m Manifest) credentialcrypto.Identity {
	return credentialcrypto.Identity{Environment: m.Environment, TenantID: "default", OwnerType: "platform", OwnerID: "offline-operator", CredentialID: m.ID, Provider: "local-operations", CredentialType: "recovery-probe", Purpose: "backup-verification", Version: 1}
}

func privateRead(path string) ([]byte, error) {
	if credentialcrypto.ValidatePrivateArtifact(path, false) != nil {
		return nil, ErrOperation
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrOperation
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4<<20))
	if err != nil || len(b) == 4<<20 {
		return nil, ErrOperation
	}
	return b, nil
}

func writeNew(path string, b []byte) error {
	f, err := credentialcrypto.CreatePrivateArtifact(path)
	if err != nil {
		return ErrOperation
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil || closed != nil {
		return ErrOperation
	}
	return nil
}

func readManifest(path string) (Manifest, error) {
	b, err := privateRead(path)
	if err != nil {
		return Manifest{}, err
	}
	defer clear(b)
	var m Manifest
	if common.Unmarshal(b, &m) != nil || m.Format != 1 || len(m.ID) != 32 || (m.Environment != "test" && m.Environment != "development" && m.Environment != "production") {
		return Manifest{}, ErrOperation
	}
	return m, nil
}

func saveManifest(path string, m Manifest) error {
	b, err := common.Marshal(m)
	if err != nil {
		return ErrOperation
	}
	return writeNew(path, b)
}

func acquire(state string) (func(), error) {
	if credentialcrypto.ValidatePrivateArtifact(state, true) != nil {
		return nil, ErrOperation
	}
	path := filepath.Join(state, "operation.lock")
	if credentialcrypto.ValidatePrivateArtifact(path, false) != nil {
		return nil, ErrOperation
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, ErrOperation
	}
	release, err := lockFile(f)
	if err != nil {
		f.Close()
		return nil, ErrOperation
	}
	return release, nil
}

func openDatabase(path string) (*gorm.DB, func(), error) {
	if credentialcrypto.ValidatePrivateArtifact(path, false) != nil {
		return nil, nil, ErrOperation
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		return nil, nil, ErrOperation
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, nil, ErrOperation
	}
	sqlDB.SetMaxOpenConns(1)
	closeDB := func() { sqlDB.Close() }
	var result string
	if db.Raw("PRAGMA quick_check").Scan(&result).Error != nil || result != "ok" {
		closeDB()
		return nil, nil, ErrOperation
	}
	return db, closeDB, nil
}

// verify authenticates every stored version, including revoked historical ones.
// This is an offline operator integrity check; it never admits a relay request
// or returns plaintext. Runtime ownership and revocation checks stay unchanged.
func verify(state string, m Manifest) error {
	p, err := credentialcrypto.NewLocalFileKeyProvider(filepath.Join(state, ringName))
	if err != nil {
		return ErrOperation
	}
	plain, err := credentialcrypto.Open(context.Background(), p, "local-"+m.Environment, probeIdentity(m), m.Probe)
	good := err == nil && string(plain) == "suanjie-recovery-probe-v1"
	clear(plain)
	if !good {
		return ErrOperation
	}
	db, closeDB, err := openDatabase(filepath.Join(state, databaseName))
	if err != nil {
		return err
	}
	defer closeDB()
	for _, table := range []any{&model.Channel{}, &model.User{}, &model.Token{}, &model.Ability{}, &model.Option{}, &model.Setup{}, &model.UserSession{}, &model.AuthFlow{}, &model.CasbinRule{}, &model.AuthzRole{}, &model.AuditLog{}, &model.Log{}, &credentials.AccessEvent{}} {
		if !db.Migrator().HasTable(table) {
			return ErrOperation
		}
	}
	var heads []credentials.Head
	var versions []credentials.StoredVersion
	if db.Find(&heads).Error != nil || db.Find(&versions).Error != nil {
		return ErrOperation
	}
	byID := make(map[string]credentials.Head, len(heads))
	for _, h := range heads {
		if h.Environment != m.Environment {
			return ErrOperation
		}
		byID[h.ID] = h
	}
	for _, v := range versions {
		h, ok := byID[v.CredentialID]
		if !ok || v.Version <= 0 {
			return ErrOperation
		}
		var envelope credentialcrypto.Envelope
		if common.UnmarshalJsonStr(v.Envelope, &envelope) != nil {
			return ErrOperation
		}
		id := credentialcrypto.Identity{Environment: h.Environment, TenantID: h.TenantID, OwnerType: h.OwnerType, OwnerID: h.OwnerID, CredentialID: h.ID, Provider: h.Provider, CredentialType: h.CredentialType, Purpose: h.Purpose, Version: uint64(v.Version)}
		secret, err := credentialcrypto.Open(context.Background(), p, "local-"+m.Environment, id, envelope)
		clear(secret)
		if err != nil {
			return ErrOperation
		}
	}
	for _, h := range heads {
		found := false
		for _, v := range versions {
			found = found || (v.CredentialID == h.ID && v.Version == h.CurrentVersion)
		}
		if !found {
			return ErrOperation
		}
	}
	var channels []model.Channel
	if db.Find(&channels).Error != nil {
		return ErrOperation
	}
	for _, channel := range channels {
		if channel.CredentialRef != "" {
			if _, ok := byID[channel.CredentialRef]; !ok || channel.Key != "" {
				return ErrOperation
			}
		}
	}
	return nil
}

// migrate operates only on an unready NEW directory. Failed DDL cannot modify
// an active source state. The ready marker is published only after verification.
func migrate(state string, m Manifest) error {
	common.SQLitePath = filepath.Join(state, databaseName)
	common.IsMasterNode = true
	common.RedisEnabled = false
	common.DebugEnabled = false
	os.Setenv("SQL_DSN", "local")
	os.Setenv("LOG_SQL_DSN", "")
	if model.InitDB() != nil {
		return ErrOperation
	}
	db := model.DB
	sqlDB, err := db.DB()
	if err != nil {
		return ErrOperation
	}
	defer sqlDB.Close()
	if model.InitLogDB() != nil {
		return ErrOperation
	}
	store := &credentials.Store{DB: db, Environment: m.Environment}
	if store.MigrateSchema() != nil {
		return ErrOperation
	}
	return nil
}

// createState reserves a new private destination. Incomplete directories have
// no ready marker and are rejected by Serve, Check and Backup. Never retry by
// deleting or replacing an existing state; select a fresh destination instead.
func createState(state string) error {
	if !filepath.IsAbs(state) || credentialcrypto.InitializeLocalDirectory(state) != nil {
		return ErrOperation
	}
	return writeNew(filepath.Join(state, "operation.lock"), nil)
}

func Initialize(state, environment string) error {
	if environment != "test" && environment != "development" && environment != "production" {
		return ErrOperation
	}
	if createState(state) != nil {
		return ErrOperation
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return ErrOperation
	}
	m := Manifest{Format: 1, Build: common.Version, Environment: environment, ID: hex.EncodeToString(id)}
	if credentialcrypto.InitializeLocalFile(filepath.Join(state, ringName)) != nil {
		return ErrOperation
	}
	p, err := credentialcrypto.NewLocalFileKeyProvider(filepath.Join(state, ringName))
	if err != nil {
		return ErrOperation
	}
	m.Probe, err = credentialcrypto.Seal(context.Background(), p, "local-"+environment, probeIdentity(m), []byte("suanjie-recovery-probe-v1"))
	if err != nil {
		return ErrOperation
	}
	if err = writeNew(filepath.Join(state, databaseName), nil); err != nil {
		return err
	}
	if err = migrate(state, m); err != nil {
		return err
	}
	if err = verify(state, m); err != nil {
		return err
	}
	return saveManifest(filepath.Join(state, readyName), m)
}

func Check(state string) error {
	release, err := acquire(state)
	if err != nil {
		return err
	}
	defer release()
	m, err := readManifest(filepath.Join(state, readyName))
	if err != nil {
		return err
	}
	return verify(state, m)
}

func fileHash(path string) (string, error) {
	if credentialcrypto.ValidatePrivateArtifact(filepath.Dir(path), true) != nil || credentialcrypto.ValidatePrivateArtifact(path, false) != nil {
		return "", ErrOperation
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ErrOperation
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", ErrOperation
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func copyNew(source, target string) error {
	if credentialcrypto.ValidatePrivateArtifact(source, false) != nil {
		return ErrOperation
	}
	in, err := os.Open(source)
	if err != nil {
		return ErrOperation
	}
	defer in.Close()
	out, err := credentialcrypto.CreatePrivateArtifact(target)
	if err != nil {
		return ErrOperation
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	closed := out.Close()
	if err != nil || closed != nil {
		return ErrOperation
	}
	return nil
}

// Backup is cold-only and serialized with ops serve using an OS advisory lock.
// Database and ring artifacts must use separate private directories. A manifest
// is written last; partial backups are unusable and never overwrite prior ones.
func Backup(state, databaseBackup, keyBackup string) error {
	if !filepath.IsAbs(databaseBackup) || !filepath.IsAbs(keyBackup) || filepath.Dir(databaseBackup) == filepath.Dir(keyBackup) {
		return ErrOperation
	}
	databaseParent, err := os.Stat(filepath.Dir(databaseBackup))
	if err != nil {
		return ErrOperation
	}
	keyParent, err := os.Stat(filepath.Dir(keyBackup))
	if err != nil || os.SameFile(databaseParent, keyParent) {
		return ErrOperation
	}
	release, err := acquire(state)
	if err != nil {
		return err
	}
	defer release()
	m, err := readManifest(filepath.Join(state, readyName))
	if err != nil {
		return err
	}
	if verify(state, m) != nil {
		return ErrOperation
	}
	db, closeDB, err := openDatabase(filepath.Join(state, databaseName))
	if err != nil {
		return err
	}
	// VACUUM INTO is a consistent SQLite snapshot, including committed WAL data.
	// Precreate with the same private ACL as key artifacts; SQLite accepts empty files.
	if writeNew(databaseBackup, nil) != nil {
		closeDB()
		return ErrOperation
	}
	err = db.Exec("VACUUM INTO ?", databaseBackup).Error
	closeDB()
	if err != nil {
		return ErrOperation
	}
	p, err := credentialcrypto.NewLocalFileKeyProvider(filepath.Join(state, ringName))
	if err != nil || p.SnapshotTo(keyBackup) != nil {
		return ErrOperation
	}
	m.DatabaseSHA256, err = fileHash(databaseBackup)
	if err != nil {
		return err
	}
	m.KeySHA256, err = fileHash(keyBackup)
	if err != nil {
		return err
	}
	return saveManifest(databaseBackup+".manifest.json", m)
}

// Restore always targets a new private directory. Rotate before publishing so
// rolling back ring nonce counters can never reuse the old active wrapping KEK.
// Old KEKs remain available to decrypt the immutable backed-up versions.
func Restore(state, databaseBackup, keyBackup string) error {
	m, err := readManifest(databaseBackup + ".manifest.json")
	if err != nil {
		return err
	}
	dbHash, err := fileHash(databaseBackup)
	if err != nil || dbHash != m.DatabaseSHA256 {
		return ErrOperation
	}
	keyHash, err := fileHash(keyBackup)
	if err != nil || keyHash != m.KeySHA256 {
		return ErrOperation
	}
	if createState(state) != nil {
		return ErrOperation
	}
	if copyNew(databaseBackup, filepath.Join(state, databaseName)) != nil {
		return ErrOperation
	}
	p, err := credentialcrypto.NewLocalFileKeyProvider(keyBackup)
	if err != nil || p.SnapshotTo(filepath.Join(state, ringName)) != nil {
		return ErrOperation
	}
	if verify(state, m) != nil {
		return ErrOperation
	}
	restored, err := credentialcrypto.NewLocalFileKeyProvider(filepath.Join(state, ringName))
	if err != nil {
		return ErrOperation
	}
	if _, err = restored.RotateKEK(context.Background()); err != nil {
		return ErrOperation
	}
	if migrate(state, m) != nil || verify(state, m) != nil {
		return ErrOperation
	}
	m.DatabaseSHA256 = ""
	m.KeySHA256 = ""
	m.Build = common.Version
	return saveManifest(filepath.Join(state, readyName), m)
}
