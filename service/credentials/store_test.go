// Copyright (c) QuantumNous and contributors.
package credentials

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestStoreDatabaseMatrix(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			var dialect gorm.Dialector
			switch kind {
			case "sqlite":
				dialect = sqlite.Open(filepath.Join(t.TempDir(), "credentials.db"))
			case "mysql":
				if os.Getenv("SUANJIE_TEST_DATABASES") != "local-synthetic" {
					t.Skip("isolated local MySQL not enabled")
				}
				dialect = mysql.Open("root:synthetic-test-only@tcp(127.0.0.1:13316)/credential_test?parseTime=true")
			case "postgres":
				if os.Getenv("SUANJIE_TEST_DATABASES") != "local-synthetic" {
					t.Skip("isolated local PostgreSQL not enabled")
				}
				dialect = postgres.Open("host=127.0.0.1 port=15436 user=postgres password=synthetic-test-only dbname=credential_test sslmode=disable")
			}
			db, err := gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			path := filepath.Join(t.TempDir(), "synthetic-ring")
			require.NoError(t, credentialcrypto.InitializeLocalFile(path))
			p, err := credentialcrypto.NewLocalFileKeyProvider(path)
			require.NoError(t, err)
			s := &Store{DB: db, Environment: "test", ProviderID: "local-test", Provider: p}
			require.NoError(t, s.MigrateSchema())
			ctx := context.Background()
			owner := Principal{TenantID: "default", UserID: "user-1"}
			h, err := s.Create(ctx, owner, false, "openai", "api_key", "relay", []byte("synthetic-one"))
			require.NoError(t, err)
			t.Cleanup(func() {
				db.Where("credential_id = ?", h.ID).Delete(&StoredVersion{})
				db.Where("id = ?", h.ID).Delete(&Head{})
			})
			audit := &syntheticAudit{}
			r := Resolver{Environment: "test", Repository: s, Providers: map[string]credentialcrypto.KeyProvider{"local-test": p}, Audit: audit}
			require.NoError(t, r.WithCredential(ctx, owner, h.ID, 1, "relay", func(b []byte) error { require.Equal(t, "synthetic-one", string(b)); return nil }))
			_, err = s.Replace(ctx, Principal{TenantID: "default", UserID: "other"}, h.ID, 1, []byte("denied"))
			require.Error(t, err)
			results := make(chan error, 8)
			for range 8 {
				go func() { _, err := s.Replace(ctx, owner, h.ID, 1, []byte("synthetic-two")); results <- err }()
			}
			winners := 0
			for range 8 {
				if <-results == nil {
					winners++
				}
			}
			require.Equal(t, 1, winners, "exactly one competing replacement may commit")
			_, err = s.Replace(ctx, owner, h.ID, 1, []byte("stale-write"))
			require.Error(t, err)
			// Force a version uniqueness conflict: the head CAS must roll back too.
			require.NoError(t, db.Create(&StoredVersion{CredentialID: h.ID, Version: 3, Envelope: "synthetic-conflict"}).Error)
			_, err = s.Replace(ctx, owner, h.ID, 2, []byte("must-rollback"))
			require.Error(t, err)
			var after Head
			require.NoError(t, db.First(&after, "id = ?", h.ID).Error)
			require.EqualValues(t, 2, after.CurrentVersion)
			require.EqualValues(t, 2, after.Revision)
			require.NoError(t, db.Delete(&StoredVersion{}, "credential_id = ? AND version = ?", h.ID, 3).Error)
			require.NoError(t, s.SetStatus(ctx, owner, h.ID, 2, "disabled"))
			_, err = s.GetForUse(ctx, h.ID, 1)
			require.Error(t, err)
			require.NoError(t, s.SetStatus(ctx, owner, h.ID, 3, "active"))
			require.NoError(t, s.SetStatus(ctx, owner, h.ID, 4, "revoked"))
			_, err = s.GetForUse(ctx, h.ID, 2)
			require.Error(t, err)
			require.Error(t, s.SetStatus(ctx, owner, h.ID, 5, "active"))
			var raw StoredVersion
			require.NoError(t, db.First(&raw, "credential_id = ? AND version = ?", h.ID, 2).Error)
			require.NotContains(t, raw.Envelope, "synthetic-two")
		})
	}
}
