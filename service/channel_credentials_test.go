// Copyright (c) QuantumNous and contributors.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/QuantumNous/new-api/service/credentials"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestManagedRequestPollingCacheAndRevocation(t *testing.T) {
	ctx := context.Background()
	previousType := common.MainDatabaseType()
	defer common.SetMainDatabaseType(previousType)
	var dialect gorm.Dialector = sqlite.Open(filepath.Join(t.TempDir(), "managed.db"))
	switch os.Getenv("SYNTHETIC_MANAGED_DATABASE") {
	case "mysql":
		require.Equal(t, "local-synthetic", os.Getenv("SUANJIE_TEST_DATABASES"))
		common.SetMainDatabaseType(common.DatabaseTypeMySQL)
		dialect = mysql.Open("root:synthetic-test-only@tcp(127.0.0.1:23316)/credential_test?parseTime=true")
	case "postgres":
		require.Equal(t, "local-synthetic", os.Getenv("SUANJIE_TEST_DATABASES"))
		common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
		dialect = postgres.Open("host=127.0.0.1 port=25436 user=postgres password=synthetic-test-only dbname=credential_test sslmode=disable")
	default:
		common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	}
	db, err := gorm.Open(dialect, &gorm.Config{})
	require.NoError(t, err)
	versionSQL := "SELECT VERSION()"
	if db.Dialector.Name() == "sqlite" {
		versionSQL = "SELECT sqlite_version()"
	}
	var engineVersion string
	require.NoError(t, db.Raw(versionSQL).Scan(&engineVersion).Error)
	t.Logf("managed database=%s engine=%s", db.Dialector.Name(), engineVersion)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	dir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, credentialcrypto.InitializeLocalDirectory(dir))
	path := filepath.Join(dir, "ring")
	require.NoError(t, credentialcrypto.InitializeLocalFile(path))
	p, err := credentialcrypto.NewLocalFileKeyProvider(path)
	require.NoError(t, err)
	s := &credentials.Store{DB: db, Environment: "test", ProviderID: "local-test", Provider: p}
	require.NoError(t, s.MigrateSchema())
	actor := credentials.Principal{TenantID: "default", UserID: "synthetic-admin", PlatformOperator: true}
	h, err := s.Create(ctx, actor, true, "openai", "channel_key_bundle", "relay", []byte("synthetic-zero\nsynthetic-disabled\nsynthetic-two"))
	require.NoError(t, err)
	ch := model.Channel{Name: "synthetic-managed", Type: constant.ChannelTypeOpenAI, CredentialRef: h.ID, Status: common.ChannelStatusEnabled, Group: "default", Models: "synthetic-model", ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 3, MultiKeyMode: constant.MultiKeyModePolling, MultiKeyStatusList: map[int]int{1: common.ChannelStatusManuallyDisabled}}}
	require.NoError(t, db.Create(&ch).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "synthetic-model", ChannelId: ch.Id, Enabled: true}).Error)
	oldDB, oldMemory := model.DB, common.MemoryCacheEnabled
	model.DB, common.MemoryCacheEnabled = db, true
	t.Cleanup(func() {
		require.NoError(t, db.Where("channel_id = ?", ch.Id).Delete(&model.Ability{}).Error)
		require.NoError(t, db.Delete(&model.Channel{}, ch.Id).Error)
		model.InitChannelCache()
		model.DB, common.MemoryCacheEnabled = oldDB, oldMemory
	})
	old := SetChannelCredentialResolver(&credentials.Resolver{Environment: "test", Repository: s, Providers: map[string]credentialcrypto.KeyProvider{"local-test": p}, Audit: s})
	t.Cleanup(func() { SetChannelCredentialResolver(old) })
	model.InitChannelCache()
	var live *gin.Context
	for i, want := range []int{0, 2, 0, 2, 0, 2} {
		cached, err := model.CacheGetChannel(ch.Id)
		require.NoError(t, err)
		require.Empty(t, cached.Key)
		require.Empty(t, cached.Keys)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		request, err := BindChannelCredentialRequest(c, cached, 123)
		require.NoError(t, err)
		key, index, keyErr := request.GetNextEnabledKey()
		require.Nil(t, keyErr)
		require.Equal(t, want, index)
		if want == 0 {
			require.Equal(t, "synthetic-zero", key)
		} else {
			require.Equal(t, "synthetic-two", key)
		}
		require.Empty(t, cached.Key)
		require.Empty(t, cached.Keys)
		if i == 0 {
			model.InitChannelCache()
		} // state survives refresh without caching plaintext
		if i == 5 {
			live = c
		} else {
			ReleaseChannelCredentialRequest(c)
		}
	}
	defer ReleaseChannelCredentialRequest(live)
	liveContext := live.Request.Context()
	second := &credentials.Store{DB: db, Environment: s.Environment, ProviderID: s.ProviderID, Provider: p}
	require.NoError(t, second.SetStatus(ctx, actor, h.ID, 1, "revoked"))
	require.Error(t, liveContext.Err())
	cached, err := model.CacheGetChannel(ch.Id)
	require.NoError(t, err)
	next, _ := gin.CreateTestContext(httptest.NewRecorder())
	next.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	_, err = BindChannelCredentialRequest(next, cached, 123)
	require.Error(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/tasks/synthetic", nil)
	_, err = BindChannelCredentialRequest(c, cached, 123)
	require.Error(t, err, "unsupported task entry never acquires a credential")
}

func TestEncryptedChannelMigrationBackupMultiKeyAndRevocation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(dir, "source.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	keyDir := filepath.Join(dir, "keys")
	require.NoError(t, credentialcrypto.InitializeLocalDirectory(keyDir))
	path := filepath.Join(keyDir, "synthetic-ring")
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
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", ch.Id).Update("other", "synthetic-unknown-config").Error)
	_, err = s.MigrateDisabledOpenAI(ctx, ch.Id, receipt)
	require.Error(t, err)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", ch.Id).Update("other", "").Error)
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("synthetic-migration-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "supplier_credential_access_events" {
			tx.AddError(credentialcrypto.ErrUnavailable)
		}
	}))
	_, err = s.MigrateDisabledOpenAI(ctx, ch.Id, receipt)
	require.Error(t, err)
	require.NoError(t, db.Callback().Create().Remove("synthetic-migration-failure"))
	var unchanged model.Channel
	require.NoError(t, db.First(&unchanged, ch.Id).Error)
	require.Equal(t, ch.Key, unchanged.Key)
	require.Empty(t, unchanged.CredentialRef)
	var orphanCount int64
	require.NoError(t, db.Model(&credentials.Head{}).Count(&orphanCount).Error)
	require.Zero(t, orphanCount)
	ref, err := s.MigrateDisabledOpenAI(ctx, ch.Id, receipt)
	require.NoError(t, err)
	refAgain, err := s.MigrateDisabledOpenAI(ctx, ch.Id, receipt)
	require.NoError(t, err)
	require.Equal(t, ref, refAgain)
	var cached model.Channel
	require.NoError(t, db.First(&cached, ch.Id).Error)
	require.Empty(t, cached.Key)
	require.Equal(t, ref, cached.CredentialRef)
	previousDB := model.DB
	model.DB = db
	defer func() { model.DB = previousDB }()
	editable := cached
	editable.Name = "renamed"
	require.NoError(t, editable.Update())
	require.Equal(t, 2, editable.ChannelInfo.MultiKeySize)
	require.Empty(t, editable.Key)
	editable.Key = "must-not-write-plaintext"
	require.Error(t, editable.Update())
	r := &credentials.Resolver{Environment: "test", Repository: s, Providers: map[string]credentialcrypto.KeyProvider{"local-test": p}, Audit: s}
	old := SetChannelCredentialResolver(r)
	defer SetChannelCredentialResolver(old)
	h := credentials.Principal{TenantID: "default", UserID: "admin", PlatformOperator: true}
	require.NoError(t, r.WithCredential(ctx, h, ref, 1, "relay", func(secret []byte) error {
		candidate := model.Channel{Key: string(secret)}
		require.Equal(t, []string{"synthetic-one", "synthetic-two"}, candidate.GetKeys())
		return nil
	}))
	require.Empty(t, cached.Key)
	require.Empty(t, cached.Keys)
	_, err = s.Replace(ctx, h, ref, 1, []byte("wrong-slot-count"))
	require.Error(t, err)
	require.NoError(t, s.SetStatus(ctx, h, ref, 1, "revoked"))
	err = r.WithCredential(ctx, h, ref, 1, "relay", func([]byte) error { t.Fatal("revoked credential used"); return nil })
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
