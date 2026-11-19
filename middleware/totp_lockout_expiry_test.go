package middleware

import (
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestExpiredTOTPFailureWindowRestartsAfterLockout(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.TwoFA{}))
	sqlDB, err := model.DB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	lockedUntil := time.Now().Add(time.Minute)
	locked := &model.TwoFA{UserId: 7001, Secret: "synthetic-seed", IsEnabled: true, FailedAttempts: 4, LockedUntil: &lockedUntil}
	require.NoError(t, model.DB.Session(&gorm.Session{SkipHooks: true}).Create(locked).Error)
	require.NoError(t, (&model.TwoFA{Id: locked.Id}).IncrementFailedAttempts())
	var whileLocked model.TwoFA
	require.NoError(t, model.DB.First(&whileLocked, locked.Id).Error)
	assert.Equal(t, 4, whileLocked.FailedAttempts, "requests during an active lock must not change the count")
	assert.True(t, time.Now().Before(*whileLocked.LockedUntil))

	expiredAt := time.Now().Add(-time.Second)
	require.NoError(t, model.DB.Model(&model.TwoFA{}).Where("id = ?", locked.Id).Updates(map[string]any{
		"failed_attempts": 5,
		"locked_until":    expiredAt,
	}).Error)
	require.NoError(t, (&model.TwoFA{Id: locked.Id}).IncrementFailedAttempts())
	var afterExpiry model.TwoFA
	require.NoError(t, model.DB.First(&afterExpiry, locked.Id).Error)
	t.Logf("expired-lock reproduction evidence: old_attempts=5 new_attempts=%d old_lock_expired=true new_lock_active=%t",
		afterExpiry.FailedAttempts, afterExpiry.LockedUntil != nil && time.Now().Before(*afterExpiry.LockedUntil))
	assert.Equal(t, 1, afterExpiry.FailedAttempts, "the first failure after expiry must start a fresh window")
	assert.Nil(t, afterExpiry.LockedUntil, "one fresh failure must not immediately relock the account")

	expiredAt = time.Now().Add(-time.Second)
	require.NoError(t, model.DB.Model(&model.TwoFA{}).Where("id = ?", locked.Id).Updates(map[string]any{
		"failed_attempts": 5,
		"locked_until":    expiredAt,
	}).Error)
	const concurrentFailures = 4
	errs := make(chan error, concurrentFailures)
	var wg sync.WaitGroup
	for range concurrentFailures {
		wg.Go(func() {
			errs <- (&model.TwoFA{Id: locked.Id}).IncrementFailedAttempts()
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var afterConcurrent model.TwoFA
	require.NoError(t, model.DB.First(&afterConcurrent, locked.Id).Error)
	assert.Equal(t, concurrentFailures, afterConcurrent.FailedAttempts)
	assert.Nil(t, afterConcurrent.LockedUntil)

	require.NoError(t, (&model.TwoFA{Id: locked.Id}).IncrementFailedAttempts())
	var relocked model.TwoFA
	require.NoError(t, model.DB.First(&relocked, locked.Id).Error)
	require.Equal(t, 5, relocked.FailedAttempts)
	require.NotNil(t, relocked.LockedUntil)
	lockedUntilAfterThreshold := *relocked.LockedUntil
	for range 3 {
		require.NoError(t, (&model.TwoFA{Id: locked.Id}).IncrementFailedAttempts())
	}
	require.NoError(t, model.DB.First(&relocked, locked.Id).Error)
	assert.Equal(t, 5, relocked.FailedAttempts, "requests during an active lock must not increase the new window")
	assert.True(t, lockedUntilAfterThreshold.Equal(*relocked.LockedUntil), "active requests must not extend the lockout")
}
