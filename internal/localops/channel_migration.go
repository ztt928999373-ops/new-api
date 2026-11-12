// Copyright (c) QuantumNous and contributors.
package localops

import (
	"context"
	"path/filepath"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/QuantumNous/new-api/service/credentials"
)

// MigrateChannel is an offline SQLite operation. A fresh paired backup and a
// successful independent restore are mandatory before the source is changed.
// No HTTP handler or automatic startup path invokes it.
func MigrateChannel(state string, channelID int, databaseBackup, keyBackup, restoreCheck string) error {
	if channelID <= 0 || !filepath.IsAbs(restoreCheck) {
		return ErrOperation
	}
	release, err := acquire(state)
	if err != nil {
		return err
	}
	defer release()
	db, closeDB, err := openDatabase(filepath.Join(state, databaseName))
	if err != nil {
		return err
	}
	defer closeDB()
	var ch model.Channel
	if db.First(&ch, channelID).Error != nil || ch.Status != common.ChannelStatusManuallyDisabled ||
		!credentials.OpenAIManagedConfiguration(ch) || (ch.CredentialRef == "" && credentials.ValidateOpenAIKeyBundle(ch, ch.Key) != nil) {
		return ErrOperation
	}
	if err := backupLocked(state, databaseBackup, keyBackup); err != nil {
		return err
	}
	if err := Restore(restoreCheck, databaseBackup, keyBackup); err != nil {
		return err
	}
	m, err := readManifest(filepath.Join(state, readyName))
	if err != nil {
		return err
	}
	p, err := credentialcrypto.NewLocalFileKeyProvider(filepath.Join(state, ringName))
	if err != nil {
		return err
	}
	digest, err := fileHash(databaseBackup)
	if err != nil {
		return err
	}
	s := &credentials.Store{DB: db, Environment: m.Environment, ProviderID: "local-" + m.Environment, Provider: p}
	_, err = s.MigrateDisabledOpenAI(context.Background(), channelID, credentials.BackupReceipt{Path: databaseBackup, SHA256: digest})
	if err != nil {
		return ErrOperation
	}
	return verify(state, m)
}
