// Copyright (c) QuantumNous and contributors.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/QuantumNous/new-api/service/credentials"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type channelCredentialTestAudit struct{}

func (channelCredentialTestAudit) Record(context.Context, credentials.Event) error { return nil }

func TestEncryptedChannelMigrationBackupMultiKeyAndRevocation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(dir, "source.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	path := filepath.Join(dir, "synthetic-ring")
	require.NoError(t, credentialcrypto.InitializeLocalFile(path))
	p, err := credentialcrypto.NewLocalFileKeyProvider(path)
	require.NoError(t, err)
	s := &credentials.Store{DB: db, Environment: "test", ProviderID: "local-test", Provider: p}
	require.NoError(t, s.MigrateSchema())
	ch := model.Channel{Type: constant.ChannelTypeOpenAI, Key: "synthetic-one\nsynthetic-two", Name: "synthetic-migration", Status: common.ChannelStatusManuallyDisabled, ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2}}
	require.NoError(t, db.Create(&ch).Error)
	_, err = s.MigrateDisabledOpenAI(ctx, ch.Id, credentials.BackupReceipt{})
	require.Error(t, err)
	backup := filepath.Join(dir, "backup.db")
	require.NoError(t, db.Exec("VACUUM INTO ?", backup).Error)
	bytes, err := os.ReadFile(backup)
	require.NoError(t, err)
	digest := sha256.Sum256(bytes)
	clear(bytes)
	receipt := credentials.BackupReceipt{Path: backup, SHA256: hex.EncodeToString(digest[:])}
	ref, err := s.MigrateDisabledOpenAI(ctx, ch.Id, receipt)
	require.NoError(t, err)
	refAgain, err := s.MigrateDisabledOpenAI(ctx, ch.Id, receipt)
	require.NoError(t, err)
	require.Equal(t, ref, refAgain)
	var cached model.Channel
	require.NoError(t, db.First(&cached, ch.Id).Error)
	require.Empty(t, cached.Key)
	require.Equal(t, ref, cached.CredentialRef)
	r := &credentials.Resolver{Environment: "test", Repository: s, Providers: map[string]credentialcrypto.KeyProvider{"local-test": p}, Audit: channelCredentialTestAudit{}}
	old := SetChannelCredentialResolver(r)
	defer SetChannelCredentialResolver(old)
	resolved, err := ResolveChannelForRequest(ctx, &cached, 123)
	require.NoError(t, err)
	require.Equal(t, []string{"synthetic-one", "synthetic-two"}, resolved.GetKeys())
	require.Empty(t, cached.Key)
	require.Empty(t, cached.Keys)
	h := credentials.Principal{TenantID: "default", UserID: "admin", PlatformOperator: true}
	require.NoError(t, s.SetStatus(ctx, h, ref, 1, "revoked"))
	_, err = ResolveChannelForRequest(ctx, &cached, 123)
	require.Error(t, err)
	// Backup remains intact and actually restores the original legacy key.
	restored, err := gorm.Open(sqlite.Open(backup), &gorm.Config{})
	require.NoError(t, err)
	conn, err := restored.DB()
	require.NoError(t, err)
	defer conn.Close()
	var original model.Channel
	require.NoError(t, restored.First(&original, ch.Id).Error)
	require.Equal(t, ch.Key, original.Key)
	_, err = os.Stat(backup)
	require.NoError(t, err)
}
