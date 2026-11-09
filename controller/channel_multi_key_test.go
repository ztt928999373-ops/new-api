package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultiKeyEnableRestoresOnlyExhaustedChannels(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	previousMaster, previousCache, previousRedis, previousSQLite := common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled, common.SQLitePath
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousType, previousLogType)
		common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled, common.SQLitePath = previousMaster, previousCache, previousRedis, previousSQLite
	})
	t.Setenv("SQL_DSN", os.Getenv("TEST_CHANNEL_SQL_DSN"))
	t.Setenv("LOG_SQL_DSN", "")
	common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled = false, false, false
	common.SQLitePath = filepath.Join(t.TempDir(), "channel.db")
	require.NoError(t, model.InitDB())
	database := model.DB
	sqlDB, err := database.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	model.LOG_DB = database
	common.SetLogDatabaseType(common.MainDatabaseType())
	require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.User{}, &model.Log{}, &model.AuditLog{}))
	root := &model.User{Username: "multi-key-review-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled}
	require.NoError(t, database.Create(root).Error)
	t.Cleanup(func() { require.NoError(t, database.Unscoped().Delete(root).Error) })
	versionQuery := "SELECT VERSION()"
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, database.Raw(versionQuery).Scan(&version).Error)
	t.Logf("database=%s version=%s", common.MainDatabaseType(), version)

	for _, cacheEnabled := range []bool{false, true} {
		for _, action := range []string{"enable_key", "enable_all_keys"} {
			for _, tc := range []struct {
				name           string
				initialStatus  int
				manualOverride string
				wantStatus     int
			}{
				{name: "key exhaustion restores", initialStatus: common.ChannelStatusEnabled, wantStatus: common.ChannelStatusEnabled},
				{name: "manual disable is preserved", initialStatus: common.ChannelStatusManuallyDisabled, wantStatus: common.ChannelStatusManuallyDisabled},
				{name: "manual disable after exhaustion is preserved", initialStatus: common.ChannelStatusEnabled, manualOverride: "status", wantStatus: common.ChannelStatusManuallyDisabled},
				{name: "tag disable after exhaustion is preserved", initialStatus: common.ChannelStatusEnabled, manualOverride: "tag", wantStatus: common.ChannelStatusManuallyDisabled},
			} {
				t.Run(fmt.Sprintf("cache=%t/%s/%s", cacheEnabled, action, tc.name), func(t *testing.T) {
					common.MemoryCacheEnabled = cacheEnabled
					tag := t.Name()
					channel := &model.Channel{Name: t.Name(), Type: 1, Key: "key-one\nkey-two", Status: tc.initialStatus, Models: "test-model", Group: "default", Tag: &tag,
						ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyStatusList: map[int]int{1: common.ChannelStatusManuallyDisabled}},
					}
					require.NoError(t, channel.Insert())
					t.Cleanup(func() {
						require.NoError(t, channel.Delete())
						model.InitChannelCache()
					})
					for _, operation := range []string{"disable_key", action} {
						if operation == action {
							if tc.manualOverride == "status" {
								model.UpdateChannelStatus(channel.Id, "", common.ChannelStatusManuallyDisabled, "manual operation")
							} else if tc.manualOverride == "tag" {
								require.NoError(t, model.DisableChannelByTag(tag))
							}
						}
						payload, err := common.Marshal(MultiKeyManageRequest{ChannelId: channel.Id, Action: operation, KeyIndex: common.GetPointer(0)})
						require.NoError(t, err)
						recorder := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(recorder)
						c.Set("id", root.Id)
						c.Set("role", common.RoleRootUser)
						c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/multi_key", bytes.NewReader(payload))
						c.Request.Header.Set("Content-Type", "application/json")
						ManageMultiKeys(c)
						var result struct {
							Success bool `json:"success"`
						}
						require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
						require.True(t, result.Success, recorder.Body.String())
					}
					loaded, err := model.GetChannelById(channel.Id, true)
					require.NoError(t, err)
					assert.Equal(t, tc.wantStatus, loaded.Status)
					assert.NotContains(t, loaded.ChannelInfo.MultiKeyStatusList, 0)
					assert.NotContains(t, loaded.ChannelInfo.MultiKeyDisabledReason, 0)
					assert.NotContains(t, loaded.ChannelInfo.MultiKeyDisabledTime, 0)
					var ability model.Ability
					require.NoError(t, database.Where("channel_id = ?", channel.Id).First(&ability).Error)
					assert.Equal(t, tc.wantStatus == common.ChannelStatusEnabled, ability.Enabled)
				})
			}
		}
	}
}

// Run the contention case in a subprocess so a lock regression cannot leave
// blocked goroutines or global locks in the remaining test suite.
func TestManageMultiKeysOverallStatusContention(t *testing.T) {
	if os.Getenv("SYNTHETIC_MULTIKEY_CONTENTION_CHILD") != "1" {
		executable, err := os.Executable()
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestManageMultiKeysOverallStatusContention$", "-test.timeout=15s")
		cmd.Env = append(os.Environ(), "SYNTHETIC_MULTIKEY_CONTENTION_CHILD=1")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))
		return
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "contention.db")), &gorm.Config{})
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.User{}, &model.AuditLog{}))
	model.DB, model.LOG_DB = db, db
	common.RedisEnabled = false
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "synthetic-contention-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled}).Error)
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled = true
	for _, first := range []string{"slot", "overall"} {
		t.Run(first, func(t *testing.T) {
			ch := model.Channel{Name: "synthetic-contention", Key: "synthetic-a\nsynthetic-b", Group: "default", Models: "synthetic", Status: common.ChannelStatusEnabled, ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2}}
			require.NoError(t, db.Create(&ch).Error)
			require.NoError(t, db.Create(&model.Ability{ChannelId: ch.Id, Group: "default", Model: "synthetic", Enabled: true}).Error)
			require.NoError(t, model.InitChannelCache())
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:contention", func(tx *gorm.DB) {
				if tx.Statement.Table == "channels" {
					once.Do(func() { close(entered); <-release })
				}
			}))
			slot := func() error {
				body, _ := common.Marshal(MultiKeyManageRequest{ChannelId: ch.Id, Action: "disable_key", KeyIndex: common.GetPointer(0)})
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Set("id", 1)
				c.Set("role", common.RoleRootUser)
				c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/multi_key/manage", bytes.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				ManageMultiKeys(c)
				var result struct {
					Success bool `json:"success"`
				}
				if err := common.Unmarshal(w.Body.Bytes(), &result); err != nil {
					return err
				}
				if !result.Success {
					return errors.New(w.Body.String())
				}
				return nil
			}
			overall := func() error {
				_, err := model.SetChannelOverallStatus([]int{ch.Id}, common.ChannelStatusManuallyDisabled, "test")
				return err
			}
			a, b := slot, overall
			if first == "overall" {
				a, b = b, a
			}
			done := make(chan error, 2)
			go func() { done <- a() }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("first writer did not reach barrier")
			}
			started := make(chan struct{})
			go func() { close(started); done <- b() }()
			<-started
			close(release)
			for range 2 {
				select {
				case err := <-done:
					require.NoError(t, err)
				case <-time.After(3 * time.Second):
					t.Fatal("writers deadlocked")
				}
			}
			require.NoError(t, db.Callback().Update().Remove("test:contention"))
			var stored model.Channel
			require.NoError(t, db.First(&stored, ch.Id).Error)
			require.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
			require.Equal(t, common.ChannelStatusManuallyDisabled, stored.ChannelInfo.MultiKeyStatusList[0])
			cached, err := model.CacheGetChannel(ch.Id)
			require.NoError(t, err)
			require.Equal(t, stored.Status, cached.Status)
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:api_refresh_failure", func(tx *gorm.DB) {
				if _, ok := tx.Statement.Dest.(*[]*model.Channel); ok {
					tx.AddError(errors.New("injected refresh failure"))
				}
			}))
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set("id", 1)
			c.Set("role", common.RoleRootUser)
			c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(ch.Id)}}
			c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/status", bytes.NewBufferString(`{"status":2}`))
			c.Request.Header.Set("Content-Type", "application/json")
			UpdateChannelStatus(c)
			require.Equal(t, http.StatusServiceUnavailable, w.Code)
			require.Contains(t, w.Body.String(), `"database_committed":true`)
			require.Contains(t, w.Body.String(), `"cache_synchronized":false`)
			require.Contains(t, w.Body.String(), `"success":false`)
			require.NoError(t, db.Callback().Query().Remove("test:api_refresh_failure"))
			require.False(t, model.IsChannelEnabledForGroupModel("default", "synthetic", ch.Id))
			require.NoError(t, model.InitChannelCache())
		})
	}
}
