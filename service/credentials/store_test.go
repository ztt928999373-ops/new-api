// Copyright (c) QuantumNous and contributors.
package credentials

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
			databasePath := filepath.Join(t.TempDir(), "credentials.db")
			dialect := syntheticCredentialDialect(t, kind, databasePath)
			db, err := gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			keyDir := filepath.Join(t.TempDir(), "keys")
			require.NoError(t, credentialcrypto.InitializeLocalDirectory(keyDir))
			path := filepath.Join(keyDir, "synthetic-ring")
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
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("synthetic-audit-failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "supplier_credential_access_events" {
					tx.AddError(credentialcrypto.ErrUnavailable)
				}
			}))
			require.Error(t, s.SetStatus(ctx, owner, h.ID, 2, "disabled"))
			require.NoError(t, db.Callback().Create().Remove("synthetic-audit-failure"))
			require.NoError(t, db.First(&after, "id = ?", h.ID).Error)
			require.Equal(t, "active", after.Status)
			require.EqualValues(t, 2, after.Revision)
			require.NoError(t, s.SetStatus(ctx, owner, h.ID, 2, "disabled"))
			_, err = s.GetForUse(ctx, h.ID, 1)
			require.Error(t, err)
			require.NoError(t, s.SetStatus(ctx, owner, h.ID, 3, "active"))
			beforeWrap, err := s.GetForUse(ctx, h.ID, 2)
			require.NoError(t, err)
			_, err = p.RotateKEK(ctx)
			require.NoError(t, err)
			require.NoError(t, s.RewrapVersion(ctx, owner, h.ID, 2, 4))
			afterWrap, err := s.GetForUse(ctx, h.ID, 2)
			require.NoError(t, err)
			require.Equal(t, beforeWrap.Envelope.Ciphertext, afterWrap.Envelope.Ciphertext)
			require.NotEqual(t, beforeWrap.Envelope.WrappedDEK.KeyVersion, afterWrap.Envelope.WrappedDEK.KeyVersion)
			require.NoError(t, r.WithCredential(ctx, owner, h.ID, 2, "relay", func(b []byte) error { require.Equal(t, "synthetic-two", string(b)); return nil }))
			require.NoError(t, s.SetStatus(ctx, owner, h.ID, 5, "revoked"))
			_, err = s.GetForUse(ctx, h.ID, 2)
			require.Error(t, err)
			require.Error(t, s.SetStatus(ctx, owner, h.ID, 6, "active"))
			var raw StoredVersion
			require.NoError(t, db.First(&raw, "credential_id = ? AND version = ?", h.ID, 2).Error)
			require.NotContains(t, raw.Envelope, "synthetic-two")
			db2, err := gorm.Open(syntheticCredentialDialect(t, kind, databasePath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sql2, err := db2.DB()
			require.NoError(t, err)
			sql2.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, sql2.Close()) })
			if kind == "sqlite" {
				require.NoError(t, db.Exec("PRAGMA busy_timeout=5000").Error)
				require.NoError(t, db2.Exec("PRAGMA busy_timeout=5000").Error)
			}
			second := &Store{DB: db2, Environment: s.Environment, ProviderID: s.ProviderID, Provider: p}
			t.Run("independent_connection_CAS", func(t *testing.T) { verifyIndependentCredentialCAS(t, s, second) })
			t.Run("admission_and_history", func(t *testing.T) { verifyCredentialAdmission(t, s, second) })

		})
	}
}

// Only fresh, explicitly provisioned local synthetic engines are accepted.
func syntheticCredentialDialect(t *testing.T, kind, path string) gorm.Dialector {
	t.Helper()
	switch kind {
	case "sqlite":
		return sqlite.Open(path)
	case "mysql":
		if os.Getenv("SUANJIE_TEST_DATABASES") != "local-synthetic" {
			t.Skip("isolated local MySQL not enabled")
		}
		return mysql.Open("root:synthetic-test-only@tcp(127.0.0.1:23316)/credential_test?parseTime=true")
	case "postgres":
		if os.Getenv("SUANJIE_TEST_DATABASES") != "local-synthetic" {
			t.Skip("isolated local PostgreSQL not enabled")
		}
		return postgres.Open("host=127.0.0.1 port=25436 user=postgres password=synthetic-test-only dbname=credential_test sslmode=disable")
	default:
		t.Fatal("unsupported synthetic dialect")
		return nil
	}
}

type stagedCredentialProvider struct {
	credentialcrypto.KeyProvider
	serial  sync.Mutex
	arrived chan struct{}
	release chan struct{}
}

func (p *stagedCredentialProvider) EncryptDEK(ctx context.Context, dek, aad []byte) (credentialcrypto.WrappedDEK, error) {
	p.serial.Lock()
	wrapped, err := p.KeyProvider.EncryptDEK(ctx, dek, aad)
	p.serial.Unlock()
	if err != nil {
		return wrapped, err
	}
	p.arrived <- struct{}{}
	select {
	case <-p.release:
		return wrapped, nil
	case <-ctx.Done():
		return credentialcrypto.WrappedDEK{}, ctx.Err()
	}
}

func verifyIndependentCredentialCAS(t *testing.T, first, second *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owner := Principal{TenantID: "default", UserID: "synthetic-cas-owner"}
	head, err := first.Create(ctx, owner, false, "openai", "api_key", "relay", []byte("synthetic-cas-one"))
	require.NoError(t, err)
	staged := &stagedCredentialProvider{KeyProvider: first.Provider, arrived: make(chan struct{}, 2), release: make(chan struct{})}
	a, b := *first, *second
	a.Provider, b.Provider = staged, staged
	results := make(chan error, 2)
	for _, store := range []*Store{&a, &b} {
		go func() {
			_, err := store.Replace(ctx, owner, head.ID, head.Revision, []byte("synthetic-cas-two"))
			results <- err
		}()
	}
	// Both independent connections have read revision 1 AND completed crypto.
	// Thus the loser is fenced by the database CAS, not by ring lock contention.
	for range 2 {
		select {
		case <-staged.arrived:
		case <-ctx.Done():
			t.Fatal("replacement did not reach database CAS barrier")
		}
	}
	close(staged.release)
	winners := 0
	for range 2 {
		if <-results == nil {
			winners++
		}
	}
	require.Equal(t, 1, winners)
	final, err := first.Metadata(ctx, owner, head.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, final.Revision)
	require.EqualValues(t, 2, final.CurrentVersion)
	var versions, replacements int64
	require.NoError(t, first.DB.Model(&StoredVersion{}).Where("credential_id = ?", head.ID).Count(&versions).Error)
	require.NoError(t, first.DB.Model(&AccessEvent{}).Where("credential_id = ? AND outcome = ?", head.ID, "replaced").Count(&replacements).Error)
	require.EqualValues(t, 2, versions)
	require.EqualValues(t, 1, replacements)
}

type syntheticHistoryGrant struct{ id, owner string }

func (g syntheticHistoryGrant) AuthorizeHistorical(_ context.Context, actor Principal, record Record, reference string) error {
	if actor.UserID != g.owner || record.Identity.CredentialID != g.id || record.Identity.Version != 1 || reference != "synthetic-pinned-task" {
		return credentialcrypto.ErrUnavailable
	}
	return nil
}

type stagedDecryptProvider struct {
	credentialcrypto.KeyProvider
	entered chan struct{}
	proceed chan struct{}
}

func (p *stagedDecryptProvider) DecryptDEK(ctx context.Context, wrapped credentialcrypto.WrappedDEK, aad []byte) ([]byte, error) {
	close(p.entered)
	select {
	case <-p.proceed:
		return p.KeyProvider.DecryptDEK(ctx, wrapped, aad)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func verifyCredentialAdmission(t *testing.T, first, second *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owner := Principal{TenantID: "default", UserID: "synthetic-admission-owner"}
	head, err := first.Create(ctx, owner, false, "openai", "api_key", "relay", []byte("synthetic-version-one"))
	require.NoError(t, err)
	r := &Resolver{Environment: first.Environment, Repository: first, Providers: map[string]credentialcrypto.KeyProvider{first.ProviderID: first.Provider}, Audit: first}
	_, err = second.Replace(ctx, owner, head.ID, 1, []byte("synthetic-version-two"))
	require.NoError(t, err)
	used := false
	use := func([]byte) error { used = true; return nil }
	require.Error(t, r.WithCredential(ctx, owner, head.ID, 1, "relay", use))
	require.False(t, used)
	require.Error(t, r.WithHistoricalCredential(ctx, owner, head.ID, 1, "relay", "synthetic-pinned-task", func(context.Context, []byte) error { return nil }))
	r.History = syntheticHistoryGrant{id: head.ID, owner: owner.UserID}
	require.Error(t, r.WithHistoricalCredential(ctx, owner, head.ID, 1, "relay", "wrong-reference", func(context.Context, []byte) error { return nil }))
	require.Error(t, r.WithCredential(ctx, Principal{TenantID: "default", UserID: "other"}, head.ID, 2, "relay", use))
	require.Error(t, r.WithCredential(ctx, Principal{TenantID: "default", UserID: "root", PlatformOperator: true}, head.ID, 2, "relay", use))
	current, err := r.AcquireCurrent(ctx, owner, head.ID, "relay", func(secret []byte) error { require.Equal(t, "synthetic-version-two", string(secret)); return nil })
	require.NoError(t, err)
	defer current.Release()
	historyStarted := make(chan context.Context, 1)
	historyDone := make(chan error, 1)
	go func() {
		historyDone <- r.WithHistoricalCredential(ctx, owner, head.ID, 1, "relay", "synthetic-pinned-task", func(useCtx context.Context, secret []byte) error {
			if string(secret) != "synthetic-version-one" {
				return credentialcrypto.ErrUnavailable
			}
			historyStarted <- useCtx
			<-useCtx.Done()
			return nil
		})
	}()
	var historical context.Context
	select {
	case historical = <-historyStarted:
	case <-ctx.Done():
		t.Fatal("historical admission did not start")
	}
	_, err = second.Replace(ctx, owner, head.ID, 2, []byte("synthetic-version-three"))
	require.NoError(t, err)
	require.Error(t, current.Context().Err(), "replacement invalidates an ordinary current-version call")
	require.NoError(t, historical.Err(), "explicit pinned history survives ordinary replacement")
	require.NoError(t, second.SetStatus(ctx, owner, head.ID, 3, "revoked"))
	require.Error(t, historical.Err(), "revocation cancels admitted history too")
	require.Error(t, <-historyDone)
	require.Error(t, r.WithCredential(ctx, owner, head.ID, 3, "relay", use))

	// Deterministic opposite ordering: admission holds the barrier while a
	// different Store instance starts revocation, then cancellation drains use.
	race, err := first.Create(ctx, owner, false, "openai", "api_key", "relay", []byte("synthetic-race"))
	require.NoError(t, err)
	blocked := &stagedDecryptProvider{KeyProvider: first.Provider, entered: make(chan struct{}), proceed: make(chan struct{})}
	racing := &Resolver{Environment: first.Environment, Repository: first, Providers: map[string]credentialcrypto.KeyProvider{first.ProviderID: blocked}, Audit: first}
	useDone := make(chan error, 1)
	go func() {
		useDone <- racing.WithCredentialContext(ctx, owner, race.ID, 1, "relay", func(useCtx context.Context, _ []byte) error { <-useCtx.Done(); return nil })
	}()
	select {
	case <-blocked.entered:
	case <-ctx.Done():
		t.Fatal("admission did not enter barrier")
	}
	revokeStarted := make(chan struct{})
	revokeDone := make(chan error, 1)
	go func() { close(revokeStarted); revokeDone <- second.SetStatus(ctx, owner, race.ID, 1, "revoked") }()
	<-revokeStarted
	close(blocked.proceed)
	require.NoError(t, <-revokeDone)
	require.Error(t, <-useDone)
	called := false
	require.Error(t, r.WithCredential(ctx, owner, race.ID, 1, "relay", func([]byte) error { called = true; return nil }))
	require.False(t, called, "no use callback is admitted after the revocation commit")
}
