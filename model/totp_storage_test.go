// Copyright (c) QuantumNous and contributors.
package model

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/glebarez/sqlite"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func TestTOTPEncryptedStorageDatabaseMatrix(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			previousDialect := common.MainDatabaseType()
			common.SetMainDatabaseType(common.DatabaseType(kind))
			if kind == "postgres" {
				common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
			}
			t.Cleanup(func() { common.SetMainDatabaseType(previousDialect) })
			var driver gorm.Dialector
			switch kind {
			case "sqlite":
				driver = sqlite.Open(":memory:")
			case "mysql":
				if os.Getenv("TEST_MYSQL_DSN") == "" {
					t.Skip("TEST_MYSQL_DSN not configured")
				}
				driver = mysql.Open(os.Getenv("TEST_MYSQL_DSN"))
			case "postgres":
				if os.Getenv("TEST_POSTGRES_DSN") == "" {
					t.Skip("TEST_POSTGRES_DSN not configured")
				}
				driver = postgres.Open(os.Getenv("TEST_POSTGRES_DSN"))
			}
			db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), NamingStrategy: schema.NamingStrategy{TablePrefix: "totp_security_"}})
			require.NoError(t, err)
			sql, err := db.DB()
			require.NoError(t, err)
			sql.SetMaxOpenConns(1)
			t.Cleanup(func() { sql.Close() })
			require.NoError(t, db.Migrator().DropTable(&TOTPStorageState{}, &TwoFA{}))
			legacySchema := struct {
				Id             int    `gorm:"primaryKey"`
				UserId         int    `gorm:"unique;not null"`
				Secret         string `gorm:"type:varchar(255);not null"`
				IsEnabled      bool
				FailedAttempts int
				LockedUntil    *time.Time
				LastUsedAt     *time.Time
				CreatedAt      time.Time
				UpdatedAt      time.Time
				DeletedAt      gorm.DeletedAt
			}{}
			require.NoError(t, db.Table("totp_security_two_fas").AutoMigrate(&legacySchema))
			require.NoError(t, db.Table("totp_security_two_fas").Create(map[string]any{"id": 9000, "user_id": 9000, "secret": "JBSWY3DPEHPK3PXP", "is_enabled": true}).Error)
			require.NoError(t, db.AutoMigrate(&TwoFA{}, &TOTPStorageState{}))
			var schemaSeed TwoFA
			require.NoError(t, db.First(&schemaSeed, 9000).Error)
			assert.True(t, schemaSeed.Secret == "JBSWY3DPEHPK3PXP")
			require.NoError(t, db.Unscoped().Delete(&schemaSeed).Error)
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&TOTPStorageState{}, &TwoFA{})) })
			dir := filepath.Join(t.TempDir(), "keys")
			require.NoError(t, credentialcrypto.InitializeLocalDirectory(dir))
			ring := filepath.Join(dir, "totp.ring")
			require.NoError(t, credentialcrypto.InitializeLocalFile(ring))
			provider, err := credentialcrypto.NewLocalFileKeyProvider(ring)
			require.NoError(t, err)
			store := &TOTPSecretStore{Environment: "test", Namespace: "synthetic-totp-database", Provider: provider}
			oldStore := SetTOTPSecretStore(store)
			oldDB := DB
			DB = db
			t.Cleanup(func() { DB = oldDB; SetTOTPSecretStore(oldStore) })
			const seed = "JBSWY3DPEHPK3PXP"
			row := TwoFA{UserId: 101, Secret: seed}
			require.NoError(t, db.Create(&row).Error)
			assert.EqualValues(t, 1, row.SecretFormat)
			assert.True(t, strings.HasPrefix(row.Secret, totpEnvelopePrefix))
			assert.False(t, strings.Contains(row.Secret, seed), "stored value contains seed")
			var stored TwoFA
			require.NoError(t, db.First(&stored, row.Id).Error)
			assert.Equal(t, row.Secret, stored.Secret)
			plain, err := store.Open(context.Background(), stored)
			require.NoError(t, err)
			assert.True(t, string(plain) == seed)
			clear(plain)
			data, err := common.Marshal(stored)
			encoded := string(data)
			require.NoError(t, err)
			assert.False(t, strings.Contains(encoded, seed))
			assert.False(t, strings.Contains(encoded, totpEnvelopePrefix))
			require.NoError(t, db.Model(&stored).Update("is_enabled", true).Error)
			stored.IsEnabled = true
			code, err := totp.GenerateCode(seed, time.Now())
			require.NoError(t, err)
			valid, err := stored.ValidateTOTPAndUpdateUsage(code)
			require.NoError(t, err)
			assert.True(t, valid)
			// These errors are infrastructure failures, not invalid codes: do not count
			// them as failed guesses and never reinterpret ciphertext as plaintext.
			for _, name := range []string{"cross_user", "cross_record", "format", "tamper", "environment", "missing_key", "wrong_key", "envelope_version"} {
				t.Run(name, func(t *testing.T) {
					bad := stored
					codec := *store
					switch name {
					case "cross_user":
						bad.UserId++
					case "cross_record":
						bad.Id++
					case "format":
						bad.SecretFormat = 99
					case "tamper":
						bad.Secret = strings.Replace(bad.Secret, "Ciphertext\":\"", "Ciphertext\":\"X", 1)
					case "environment":
						codec.Environment = "production"
					case "wrong_key":
						other := filepath.Join(t.TempDir(), "other")
						require.NoError(t, credentialcrypto.InitializeLocalDirectory(other))
						ring := filepath.Join(other, "totp.ring")
						require.NoError(t, credentialcrypto.InitializeLocalFile(ring))
						var e error
						codec.Provider, e = credentialcrypto.NewLocalFileKeyProvider(ring)
						require.NoError(t, e)
					case "envelope_version":
						bad.Secret = strings.Replace(bad.Secret, "FormatVersion\":1", "FormatVersion\":99", 1)
					case "missing_key":
						codec.Provider = nil
					}
					_, e := codec.Open(context.Background(), bad)
					assert.ErrorIs(t, e, ErrTOTPStorage)
				})
			}
			before := stored.FailedAttempts
			previous := SetTOTPSecretStore(nil)
			_, err = stored.ValidateTOTPAndUpdateUsage(code)
			SetTOTPSecretStore(previous)
			assert.ErrorIs(t, err, ErrTOTPStorage)
			assert.Equal(t, before, stored.FailedAttempts)
			// Legacy fixtures intentionally bypass hooks. Pending/active/soft-deleted
			// records all migrate, without modifying their authentication state.
			until := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
			legacy := []TwoFA{{Id: 1001, UserId: 1001, Secret: seed, IsEnabled: true, FailedAttempts: 3, LockedUntil: &until}, {Id: 1002, UserId: 1002, Secret: seed}, {Id: 1003, UserId: 1003, Secret: seed, DeletedAt: gorm.DeletedAt{Time: time.Now().UTC().Truncate(time.Second), Valid: true}}}
			require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&legacy).Error)
			var beforeMigration TwoFA
			require.NoError(t, db.First(&beforeMigration, 1001).Error)
			stopped := errors.New("synthetic interruption")
			require.ErrorIs(t, MigrateTOTP(db, store, func(id int) error {
				if id == 1001 {
					return stopped
				}
				return nil
			}), stopped)
			require.ErrorIs(t, TOTPReady(db, store), ErrTOTPStorage)
			var first TwoFA
			require.NoError(t, db.First(&first, 1001).Error)
			firstCipher := first.Secret
			assert.EqualValues(t, 1, first.SecretFormat)
			assert.True(t, first.UpdatedAt.Equal(beforeMigration.UpdatedAt), "storage migration must preserve authentication timestamps")
			assert.Equal(t, 3, first.FailedAttempts)
			assert.True(t, first.IsEnabled)
			assert.True(t, first.LockedUntil.Equal(until))
			var pending TwoFA
			require.NoError(t, db.First(&pending, 1002).Error)
			assert.Zero(t, pending.SecretFormat)
			require.NoError(t, MigrateTOTP(db, store, nil))
			require.NoError(t, PublishTOTPReady(db, store))
			require.NoError(t, TOTPReady(db, store))
			require.NoError(t, db.First(&first, 1001).Error)
			assert.Equal(t, firstCipher, first.Secret, "resume must not encrypt known ciphertext twice")
			inventory, err := InventoryTOTP(db, store)
			require.NoError(t, err)
			assert.Zero(t, inventory.Plaintext)
			assert.Zero(t, inventory.Anomalous)
			assert.Equal(t, 4, inventory.Encrypted)
			assert.Equal(t, 1, inventory.Deleted)
			_, err = provider.RotateKEK(context.Background())
			require.NoError(t, err)
			require.NoError(t, RewrapTOTP(db, store))
			require.NoError(t, TOTPReady(db, store))
			columns, err := db.Migrator().ColumnTypes(&TwoFA{})
			require.NoError(t, err)
			found := false
			for _, c := range columns {
				if c.Name() == "secret" {
					found = true
					assert.Contains(t, strings.ToLower(c.DatabaseTypeName()), "text")
				}
			}
			assert.True(t, found)
			// A deterministic interleaving changes the compared field immediately before
			// CAS. The transaction must roll back, with no partially converted row.
			conflict := TwoFA{Id: 2001, UserId: 2001, Secret: seed}
			require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&conflict).Error)
			fired := false
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("totp_cas_conflict", func(tx *gorm.DB) {
				if !fired && tx.Statement.Table == "totp_security_two_fas" {
					if m, ok := tx.Statement.Dest.(map[string]any); ok && m["secret"] != nil {
						fired = true
						tx.AddError(tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}).Model(&TwoFA{}).Where("id = ?", 2001).Update("secret", "KRSXG5DSNFXGOIDB").Error)
					}
				}
			}))
			assert.ErrorIs(t, MigrateTOTP(db, store, nil), ErrTOTPStorage)
			require.NoError(t, db.Callback().Update().Remove("totp_cas_conflict"))
			require.NoError(t, db.First(&conflict, 2001).Error)
			assert.Zero(t, conflict.SecretFormat)
			assert.True(t, conflict.Secret == seed)
			require.NoError(t, MigrateTOTP(db, store, nil))
			require.NoError(t, PublishTOTPReady(db, store))
		})
	}
}
