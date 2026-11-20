package middleware

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func TestIncrementFailedAttemptsMultiConnectionDatabaseMatrix(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			previousDB := model.DB
			previousDialect := common.MainDatabaseType()
			t.Cleanup(func() {
				model.DB = previousDB
				common.SetMainDatabaseType(previousDialect)
			})

			common.SetMainDatabaseType(common.DatabaseType(kind))
			var dialector gorm.Dialector
			switch kind {
			case "sqlite":
				dialector = sqlite.Open(filepath.Join(t.TempDir(), "totp-lockout.db"))
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				dialector = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				dialector = postgres.Open(dsn)
			}

			db, err := gorm.Open(dialector, &gorm.Config{
				Logger:         logger.Default.LogMode(logger.Silent),
				NamingStrategy: schema.NamingStrategy{TablePrefix: "totp_lockout_"},
			})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(16)
			sqlDB.SetMaxIdleConns(16)
			t.Cleanup(func() {
				require.NoError(t, db.Migrator().DropTable(&model.TwoFA{}))
				require.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.AutoMigrate(&model.TwoFA{}))
			model.DB = db

			locked := time.Now().Add(-time.Minute)
			row := model.TwoFA{UserId: 88001, Secret: "synthetic-seed", IsEnabled: true, FailedAttempts: common.MaxFailAttempts, LockedUntil: &locked}
			require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&row).Error)
			require.NoError(t, (&model.TwoFA{Id: row.Id}).IncrementFailedAttempts())
			var afterExpiry model.TwoFA
			require.NoError(t, db.First(&afterExpiry, row.Id).Error)
			require.Equal(t, 1, afterExpiry.FailedAttempts)
			require.Nil(t, afterExpiry.LockedUntil)

			// Make every worker read the same expired state before any CAS update proceeds.
			locked = time.Now().Add(-time.Minute)
			require.NoError(t, db.Model(&model.TwoFA{}).Where("id = ?", row.Id).Updates(map[string]any{
				"failed_attempts": common.MaxFailAttempts,
				"locked_until":    locked,
			}).Error)
			const workers = 4
			var gateEnabled atomic.Bool
			var initialReads atomic.Int32
			var conflicts atomic.Int32
			var barrierWorkers atomic.Int32
			gate := make(chan struct{})
			queryCallback := "test:totp-lockout-query-barrier"
			updateCallback := "test:totp-lockout-conflict-count"
			require.NoError(t, db.Callback().Query().After("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
				if !gateEnabled.Load() || tx.Statement.Table != "totp_lockout_two_fas" {
					return
				}
				readNumber := initialReads.Add(1)
				if readNumber <= barrierWorkers.Load() {
					if readNumber == barrierWorkers.Load() {
						close(gate)
					}
					<-gate
				}
			}))
			require.NoError(t, db.Callback().Update().After("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
				if gateEnabled.Load() && tx.Statement.Table == "totp_lockout_two_fas" && tx.RowsAffected == 0 {
					conflicts.Add(1)
				}
			}))
			t.Cleanup(func() {
				require.NoError(t, db.Callback().Query().Remove(queryCallback))
				require.NoError(t, db.Callback().Update().Remove(updateCallback))
			})
			barrierWorkers.Store(workers)
			gateEnabled.Store(true)
			errCh := make(chan error, workers)
			var wg sync.WaitGroup
			for range workers {
				wg.Go(func() { errCh <- (&model.TwoFA{Id: row.Id}).IncrementFailedAttempts() })
			}
			wg.Wait()
			close(errCh)
			for err := range errCh {
				require.NoError(t, err)
			}
			gateEnabled.Store(false)
			var afterConcurrent model.TwoFA
			require.NoError(t, db.First(&afterConcurrent, row.Id).Error)
			require.Equal(t, workers, afterConcurrent.FailedAttempts, "one reset plus each later failure must be committed")
			require.GreaterOrEqual(t, conflicts.Load(), int32(workers-1), "stale initial CAS attempts must be observed")
			require.Nil(t, afterConcurrent.LockedUntil)

			locked = time.Now().Add(-time.Minute)
			require.NoError(t, db.Model(&model.TwoFA{}).Where("id = ?", row.Id).Updates(map[string]any{
				"failed_attempts": common.MaxFailAttempts,
				"locked_until":    locked,
			}).Error)
			initialReads.Store(0)
			conflicts.Store(0)
			gate = make(chan struct{})
			barrierWorkers.Store(int32(common.MaxFailAttempts))
			gateEnabled.Store(true)
			thresholdErrs := make(chan error, common.MaxFailAttempts)
			var thresholdWG sync.WaitGroup
			for range common.MaxFailAttempts {
				thresholdWG.Go(func() { thresholdErrs <- (&model.TwoFA{Id: row.Id}).IncrementFailedAttempts() })
			}
			thresholdWG.Wait()
			close(thresholdErrs)
			for err := range thresholdErrs {
				require.NoError(t, err)
			}
			gateEnabled.Store(false)
			var relocked model.TwoFA
			require.NoError(t, db.First(&relocked, row.Id).Error)
			require.Equal(t, common.MaxFailAttempts, relocked.FailedAttempts)
			require.NotNil(t, relocked.LockedUntil)
			require.True(t, time.Now().Before(*relocked.LockedUntil))
			require.GreaterOrEqual(t, conflicts.Load(), int32(common.MaxFailAttempts-1), "threshold requests must contend on the same expired state")

			lockedDeadline := *relocked.LockedUntil
			var activeWG sync.WaitGroup
			activeErrs := make(chan error, workers)
			for range workers {
				activeWG.Go(func() { activeErrs <- (&model.TwoFA{Id: row.Id}).IncrementFailedAttempts() })
			}
			activeWG.Wait()
			close(activeErrs)
			for err := range activeErrs {
				require.NoError(t, err)
			}
			require.NoError(t, db.First(&relocked, row.Id).Error)
			require.Equal(t, common.MaxFailAttempts, relocked.FailedAttempts)
			require.True(t, lockedDeadline.Equal(*relocked.LockedUntil), "active lock must not be extended")

			// Force every CAS to lose to an independent committed update.
			require.NoError(t, db.Model(&model.TwoFA{}).Where("id = ?", row.Id).Updates(map[string]any{
				"failed_attempts": 0,
				"locked_until":    nil,
			}).Error)
			const maxRetries = 5
			var forcedConflicts atomic.Int32
			forceCallback := "test:totp-lockout-force-cas-conflict"
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register(forceCallback, func(tx *gorm.DB) {
				if tx.Statement.Table != "totp_lockout_two_fas" || forcedConflicts.Load() >= maxRetries {
					return
				}
				forcedConflicts.Add(1)
				if err := db.Exec("UPDATE totp_lockout_two_fas SET failed_attempts = failed_attempts + 1 WHERE id = ?", row.Id).Error; err != nil {
					tx.AddError(fmt.Errorf("synthetic conflict injection failed: %w", err))
				}
			}))
			conflictErr := (&model.TwoFA{Id: row.Id}).IncrementFailedAttempts()
			require.ErrorContains(t, conflictErr, "conflicting updates to the 2FA failure count")
			require.EqualValues(t, maxRetries, forcedConflicts.Load())
			require.NoError(t, db.Callback().Update().Remove(forceCallback))
			var afterForcedConflicts model.TwoFA
			require.NoError(t, db.First(&afterForcedConflicts, row.Id).Error)
			require.Equal(t, maxRetries, afterForcedConflicts.FailedAttempts, "all injected competing updates must remain accounted for")
		})
	}
}
