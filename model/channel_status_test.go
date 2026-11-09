package model

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestOverallChannelStatusIndependentOfCredentials(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			previousType := common.MainDatabaseType()
			switch kind {
			case "mysql":
				common.SetMainDatabaseType(common.DatabaseTypeMySQL)
			case "postgres":
				common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
			default:
				common.SetMainDatabaseType(common.DatabaseTypeSQLite)
			}
			defer common.SetMainDatabaseType(previousType)
			var dialect gorm.Dialector = sqlite.Open(filepath.Join(t.TempDir(), "overall.db"))
			if kind != "sqlite" {
				if os.Getenv("SUANJIE_TEST_DATABASES") != "local-synthetic" {
					t.Skip("isolated synthetic database disabled")
				}
				if kind == "mysql" {
					dialect = mysql.Open("root:synthetic-test-only@tcp(127.0.0.1:23316)/credential_test?parseTime=true")
				} else {
					dialect = postgres.Open("host=127.0.0.1 port=25436 user=postgres password=synthetic-test-only dbname=credential_test sslmode=disable")
				}
			}
			db, err := gorm.Open(dialect, &gorm.Config{})
			require.NoError(t, err)
			connection, err := db.DB()
			require.NoError(t, err)
			defer connection.Close()
			require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
			old, oldMemory := DB, common.MemoryCacheEnabled
			DB = db
			common.MemoryCacheEnabled = true
			defer func() { DB = old; common.MemoryCacheEnabled = oldMemory }()
			t.Run("refresh_barriers", testChannelRefreshSerializesStatusWriters)
			t.Run("committed_refresh_failure", testCommittedChannelRefreshFailure)
			for _, ref := range []string{"", "synthetic-reference"} {
				ch := Channel{Name: "synthetic-overall", Group: "synthetic", Models: "synthetic", Key: "", CredentialRef: ref, Status: common.ChannelStatusManuallyDisabled, ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyPollingIndex: 1, MultiKeyStatusList: map[int]int{0: common.ChannelStatusAutoDisabled}}}
				require.NoError(t, db.Create(&ch).Error)
				defer func() { db.Where("channel_id = ?", ch.Id).Delete(&Ability{}); db.Delete(&Channel{}, ch.Id) }()
				require.NoError(t, db.Create(&Ability{ChannelId: ch.Id, Group: "synthetic", Model: "synthetic", Enabled: false}).Error)
				for _, status := range []int{common.ChannelStatusEnabled, common.ChannelStatusManuallyDisabled, common.ChannelStatusEnabled, common.ChannelStatusEnabled} {
					_, err := SetChannelOverallStatus([]int{ch.Id}, status, "manual test")
					require.NoError(t, err)
					var stored Channel
					require.NoError(t, db.First(&stored, ch.Id).Error)
					require.Equal(t, status, stored.Status)
					require.Equal(t, ch.ChannelInfo, stored.ChannelInfo)
					var ability Ability
					require.NoError(t, db.Where("channel_id = ?", ch.Id).First(&ability).Error)
					require.Equal(t, status == common.ChannelStatusEnabled, ability.Enabled)
				}
				start := make(chan struct{})
				results := make(chan error, 2)
				for _, status := range []int{common.ChannelStatusEnabled, common.ChannelStatusManuallyDisabled} {
					go func() { <-start; _, e := SetChannelOverallStatus([]int{ch.Id}, status, "concurrent"); results <- e }()
				}
				close(start)
				require.NoError(t, <-results)
				require.NoError(t, <-results)
				var stored Channel
				require.NoError(t, db.First(&stored, ch.Id).Error)
				var ability Ability
				require.NoError(t, db.Where("channel_id = ?", ch.Id).First(&ability).Error)
				require.Equal(t, stored.Status == common.ChannelStatusEnabled, ability.Enabled)
				require.Equal(t, ch.ChannelInfo, stored.ChannelInfo)
				cached, err := CacheGetChannel(ch.Id)
				require.NoError(t, err)
				require.Equal(t, stored.Status, cached.Status)
				target := common.ChannelStatusEnabled
				if stored.Status == target {
					target = common.ChannelStatusManuallyDisabled
				}
				_, err = SetChannelOverallStatus([]int{ch.Id, 2147483647}, target, "must rollback")
				require.Error(t, err)
				var after Channel
				require.NoError(t, db.First(&after, ch.Id).Error)
				require.Equal(t, stored.Status, after.Status)
			}
		})
	}
}

func setupChannelStatusTest(t *testing.T) {
	t.Helper()
	truncateTables(t)
	require.NoError(t, DB.Exec("DELETE FROM abilities").Error)
	require.NoError(t, DB.Exec("DELETE FROM channels").Error)

	memoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = memoryCacheEnabled
	})
}

func TestUpdateChannelStatusPersistsMultiKeyState(t *testing.T) {
	setupChannelStatusTest(t)

	channel := Channel{
		Name:   "multi-key-status",
		Key:    "key-a\nkey-b",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:           true,
			MultiKeySize:         2,
			MultiKeyMode:         constant.MultiKeyModePolling,
			MultiKeyPollingIndex: 1,
		},
	}
	require.NoError(t, DB.Create(&channel).Error)

	changed := UpdateChannelStatus(channel.Id, "key-a", common.ChannelStatusAutoDisabled, "provider rejected key")
	require.True(t, changed)

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.ChannelInfo.MultiKeyStatusList[0])
	assert.Equal(t, "provider rejected key", stored.ChannelInfo.MultiKeyDisabledReason[0])
	assert.NotZero(t, stored.ChannelInfo.MultiKeyDisabledTime[0])
	assert.Equal(t, 1, stored.ChannelInfo.MultiKeyPollingIndex)
}

func TestSaveStatusStateFromSingleKeySnapshotPreservesUnownedColumns(t *testing.T) {
	setupChannelStatusTest(t)

	channel := Channel{
		Name:        "single-key-status",
		Key:         "original-key",
		Status:      common.ChannelStatusEnabled,
		Models:      "original-model",
		Group:       "default",
		UsedQuota:   100,
		ChannelInfo: ChannelInfo{},
	}
	require.NoError(t, DB.Create(&channel).Error)

	stale, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)

	concurrentChannelInfo := ChannelInfo{
		IsMultiKey:           true,
		MultiKeySize:         2,
		MultiKeyMode:         constant.MultiKeyModePolling,
		MultiKeyPollingIndex: 1,
	}
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
		"key":          "rotated-key",
		"used_quota":   gorm.Expr("used_quota + ?", 250),
		"models":       "concurrent-model",
		"channel_info": concurrentChannelInfo,
	}).Error)

	stale.Status = common.ChannelStatusManuallyDisabled
	stale.SetOtherInfo(map[string]any{
		"status_reason": "manual operation",
		"status_time":   int64(1234),
	})
	require.NoError(t, stale.saveStatusState())

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
	assert.Equal(t, "rotated-key", stored.Key)
	assert.Equal(t, int64(350), stored.UsedQuota)
	assert.Equal(t, "concurrent-model", stored.Models)
	assert.Equal(t, concurrentChannelInfo, stored.ChannelInfo)

	otherInfo := stored.GetOtherInfo()
	assert.Equal(t, "manual operation", otherInfo["status_reason"])
	assert.Equal(t, float64(1234), otherInfo["status_time"])
}

// Block a refresh after its channel query has captured a snapshot, then queue
// a writer. No sleeps: query callbacks and channels establish the interleaving.
func testChannelRefreshSerializesStatusWriters(t *testing.T) {
	for _, operation := range []string{"disable", "enable", "slot"} {
		t.Run(operation, func(t *testing.T) {
			initial := common.ChannelStatusEnabled
			target := common.ChannelStatusManuallyDisabled
			if operation == "enable" {
				initial, target = target, initial
			}
			ch := Channel{Name: "refresh-barrier", Key: "synthetic-a\nsynthetic-b", Group: "default", Models: "synthetic", Status: initial, ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeySize: 2}}
			require.NoError(t, DB.Create(&ch).Error)
			t.Cleanup(func() { DB.Where("channel_id = ?", ch.Id).Delete(&Ability{}); DB.Delete(&Channel{}, ch.Id) })
			require.NoError(t, DB.Create(&Ability{ChannelId: ch.Id, Group: "default", Model: "synthetic", Enabled: initial == common.ChannelStatusEnabled}).Error)
			InitChannelCache()
			captured, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			name := "test:refresh_snapshot"
			require.NoError(t, DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
				if _, ok := tx.Statement.Dest.(*[]*Channel); ok {
					once.Do(func() { close(captured); <-release })
				}
			}))
			refreshed := make(chan struct{})
			go func() { InitChannelCache(); close(refreshed) }()
			<-captured
			// The read barrier must still own the publication fence. This
			// assertion makes the old implementation fail deterministically.
			unlocked := channelStatusLock.TryLock()
			if unlocked {
				channelStatusLock.Unlock()
			}
			assert.False(t, unlocked, "snapshot reads must hold the status fence")
			started, done := make(chan struct{}), make(chan error, 1)
			go func() {
				close(started)
				if operation == "slot" {
					if !UpdateChannelStatus(ch.Id, "synthetic-a", common.ChannelStatusAutoDisabled, "test") {
						done <- errors.New("slot failed")
						return
					}
					done <- nil
				} else {
					_, err := SetChannelOverallStatus([]int{ch.Id}, target, "test")
					done <- err
				}
			}()
			<-started
			close(release)
			<-refreshed
			require.NoError(t, <-done)
			require.NoError(t, DB.Callback().Query().Remove(name))
			var stored Channel
			require.NoError(t, DB.First(&stored, ch.Id).Error)
			cached, err := CacheGetChannel(ch.Id)
			require.NoError(t, err)
			require.Equal(t, stored.Status, cached.Status)
			require.Equal(t, stored.ChannelInfo.MultiKeyStatusList, cached.ChannelInfo.MultiKeyStatusList)
			if operation == "slot" {
				require.Equal(t, common.ChannelStatusAutoDisabled, stored.ChannelInfo.MultiKeyStatusList[0])
			} else {
				require.Equal(t, target, stored.Status)
			}
			require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("test:fail_status", func(tx *gorm.DB) { tx.AddError(errors.New("injected write failure")) }))
			_, err = SetChannelOverallStatus([]int{ch.Id}, common.ChannelStatusManuallyDisabled, "failure")
			require.Error(t, err)
			require.False(t, UpdateChannelStatus(ch.Id, "synthetic-b", common.ChannelStatusAutoDisabled, "failure"))
			require.NoError(t, DB.Callback().Update().Remove("test:fail_status"))
			after, err := CacheGetChannel(ch.Id)
			require.NoError(t, err)
			require.Equal(t, stored.Status, after.Status)
			require.Equal(t, stored.ChannelInfo.MultiKeyStatusList, after.ChannelInfo.MultiKeyStatusList)
			var persisted Channel
			require.NoError(t, DB.First(&persisted, ch.Id).Error)
			require.Equal(t, stored.Status, persisted.Status)
			require.Equal(t, stored.ChannelInfo, persisted.ChannelInfo)
			var ability Ability
			require.NoError(t, DB.Where("channel_id = ?", ch.Id).First(&ability).Error)
			require.Equal(t, stored.Status == common.ChannelStatusEnabled, ability.Enabled)
		})
	}
}

func testCommittedChannelRefreshFailure(t *testing.T) {
	ch := Channel{Name: "synthetic-refresh-failure", Key: "synthetic", Group: "default", Models: "synthetic-failure", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&ch).Error)
	t.Cleanup(func() { DB.Where("channel_id = ?", ch.Id).Delete(&Ability{}); DB.Delete(&Channel{}, ch.Id) })
	require.NoError(t, DB.Create(&Ability{ChannelId: ch.Id, Group: "default", Model: "synthetic-failure", Enabled: true}).Error)
	healthy := Channel{Name: "synthetic-healthy", Key: "synthetic", Group: "default", Models: "synthetic-healthy", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&healthy).Error)
	require.NoError(t, DB.Create(&Ability{ChannelId: healthy.Id, Group: "default", Model: "synthetic-healthy", Enabled: true}).Error)
	t.Cleanup(func() { DB.Where("channel_id = ?", healthy.Id).Delete(&Ability{}); DB.Delete(&Channel{}, healthy.Id) })
	for _, table := range []string{"channels", "abilities"} {
		_, err := SetChannelOverallStatus([]int{ch.Id}, common.ChannelStatusEnabled, "setup")
		require.NoError(t, err)
		require.NoError(t, DB.Callback().Query().Before("gorm:query").Register("test:fail_refresh", func(tx *gorm.DB) {
			switch tx.Statement.Dest.(type) {
			case *[]*Channel, *[]*Ability:
				if tx.Statement.Table == table {
					tx.AddError(errors.New("injected cache query failure"))
				}
			}
		}))
		_, err = SetChannelOverallStatus([]int{ch.Id}, common.ChannelStatusManuallyDisabled, "disable")
		require.ErrorIs(t, err, ErrChannelCacheSync)
		require.NoError(t, DB.Callback().Query().Remove("test:fail_refresh"))
		var stored Channel
		require.NoError(t, DB.First(&stored, ch.Id).Error)
		require.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
		require.False(t, IsChannelEnabledForGroupModel("default", "synthetic-failure", ch.Id))
		_, err = CacheGetChannel(ch.Id)
		require.Error(t, err)
		selected, err := GetRandomSatisfiedChannel("default", "synthetic-failure", 0, nil)
		require.NoError(t, err)
		require.Nil(t, selected)
		require.NoError(t, InitChannelCache())
		require.True(t, IsChannelEnabledForGroupModel("default", "synthetic-healthy", healthy.Id))
		require.False(t, IsChannelEnabledForGroupModel("default", "synthetic-failure", ch.Id))
	}
}
