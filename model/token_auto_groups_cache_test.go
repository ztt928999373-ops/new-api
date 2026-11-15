package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Reads remain available while a selected invalidation command fails. This
// models partial Redis failure without hiding the stale entry behind an outage.
type tokenInvalidationFailureHook struct {
	command string
	err     error
}

func (h tokenInvalidationFailureHook) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	if cmd.Name() == h.command {
		return ctx, h.err
	}
	return ctx, nil
}

func (tokenInvalidationFailureHook) AfterProcess(context.Context, redis.Cmder) error {
	return nil
}

func (tokenInvalidationFailureHook) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (tokenInvalidationFailureHook) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}

type tokenAuthorizationReadBarrier struct {
	tokenInvalidationFailureHook
	started chan struct{}
	release chan struct{}
	blocked atomic.Bool
}

func (h *tokenAuthorizationReadBarrier) AfterProcess(_ context.Context, cmd redis.Cmder) error {
	if cmd.Name() == "hgetall" && h.blocked.CompareAndSwap(false, true) {
		close(h.started)
		<-h.release
	}
	return nil
}

func TestTokenRevocationWithOverlappingCachedAuthentication(t *testing.T) {
	useUserCacheMiniRedis(t)
	token := Token{UserId: 7, Key: "synthetic-overlapping-revocation", Name: "overlapping-revocation", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}
	require.NoError(t, token.Insert())
	db := DB
	t.Cleanup(func() { require.NoError(t, db.Unscoped().Delete(&token).Error) })
	require.NoError(t, cacheSetTokenForTest(token))
	hook := &tokenAuthorizationReadBarrier{
		tokenInvalidationFailureHook: tokenInvalidationFailureHook{command: "del", err: errors.New("synthetic invalidation failure")},
		started:                      make(chan struct{}), release: make(chan struct{}),
	}
	common.RDB.AddHook(hook)
	var release sync.Once
	done := make(chan struct{})
	t.Cleanup(func() {
		release.Do(func() { close(hook.release) })
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("synthetic authentication worker did not stop")
		}
	})
	finished := make(chan error, 1)
	go func() {
		defer close(done)
		_, err := ValidateUserToken(token.Key)
		finished <- err
	}()
	select {
	case <-hook.started:
	case <-time.After(5 * time.Second):
		t.Fatal("synthetic authentication did not reach the Redis read barrier")
	}
	token.Status = common.TokenStatusDisabled
	require.NoError(t, token.Update())
	release.Do(func() { close(hook.release) })
	select {
	case <-finished:
		// A request which began before revocation is in flight. Its outcome is
		// deliberately not used as evidence about new authorization decisions.
	case <-time.After(5 * time.Second):
		t.Fatal("overlapping synthetic authentication did not complete")
	}
	_, err := ValidateUserToken(token.Key)
	require.ErrorIs(t, err, ErrTokenInvalid, "a new authentication after committed revocation must be denied")
}

// Assert the security contract instead of making stale authorization an expected
// result. Execute this test before and after any fix; baseline runtime results
// have not yet been established.
func TestTokenRevocationRejectsStaleRedisAuthorization(t *testing.T) {
	for _, mutation := range []string{"disable", "delete", "batch_delete"} {
		for _, failure := range []string{"none", "set", "del", "timeout", "outage_recovery", "redis_disabled"} {
			t.Run(mutation+"/"+failure, func(t *testing.T) {
				server := useUserCacheMiniRedis(t)
				token := Token{
					UserId: 7, Key: fmt.Sprintf("synthetic-revoke-%s-%s", mutation, failure),
					Name: "synthetic-revocation", Status: common.TokenStatusEnabled,
					ExpiredTime: -1, UnlimitedQuota: true,
				}
				require.NoError(t, token.Insert())
				db := DB
				t.Cleanup(func() { require.NoError(t, db.Unscoped().Delete(&token).Error) })
				require.NoError(t, cacheSetTokenForTest(token))
				_, err := ValidateUserToken(token.Key)
				require.NoError(t, err, "a valid synthetic key must authenticate before revocation")
				switch failure {
				case "set", "del":
					common.RDB.AddHook(tokenInvalidationFailureHook{command: failure, err: errors.New("synthetic invalidation failure")})
				case "timeout":
					common.RDB.AddHook(tokenInvalidationFailureHook{command: "set", err: context.DeadlineExceeded})
				case "outage_recovery":
					server.SetError("ERR synthetic Redis unavailable")
				case "redis_disabled":
					common.RedisEnabled = false
				}
				switch mutation {
				case "disable":
					token.Status = common.TokenStatusDisabled
					err = token.Update()
				case "delete":
					err = token.Delete()
				case "batch_delete":
					var count int
					count, err = BatchDeleteTokens([]int{token.Id}, token.UserId)
					if err == nil {
						require.Equal(t, 1, count)
					}
				}
				require.NoError(t, err, "only successfully committed revocations are tested here")
				server.SetError("")
				stored, dbErr := GetTokenByIds(token.Id, token.UserId)
				if mutation == "disable" {
					require.NoError(t, dbErr)
					require.Equal(t, common.TokenStatusDisabled, stored.Status)
				} else {
					require.ErrorIs(t, dbErr, gorm.ErrRecordNotFound)
				}
				_, err = ValidateUserToken(token.Key)
				require.ErrorIs(t, err, ErrTokenInvalid, "committed revocation must deny a new authentication even with a stale Redis entry")
			})
		}
	}
}

func TestTokenRevocationDatabaseMatrix(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			var dialect gorm.Dialector = sqlite.Open(filepath.Join(t.TempDir(), "revocation.sqlite"))
			if kind != "sqlite" {
				if os.Getenv("SUANJIE_TEST_DATABASES") != "local-synthetic" {
					t.Skip("isolated synthetic databases not configured")
				}
				if kind == "mysql" {
					dialect = mysql.Open("root:synthetic-test-only@tcp(127.0.0.1:23316)/credential_test?parseTime=true")
				} else {
					dialect = postgres.Open("host=127.0.0.1 port=25436 user=postgres password=synthetic-test-only dbname=credential_test sslmode=disable")
				}
			}
			// Only a uniquely named synthetic Token table is created/dropped.
			db, err := gorm.Open(dialect, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: fmt.Sprintf("p101_%x_", time.Now().UnixNano())}})
			require.NoError(t, err)
			connection, err := db.DB()
			require.NoError(t, err)
			connection.SetMaxOpenConns(1)
			previous := DB
			DB = db
			t.Cleanup(func() {
				DB = previous
				require.NoError(t, db.Migrator().DropTable(&Token{}))
				require.NoError(t, connection.Close())
			})
			require.NoError(t, db.AutoMigrate(&Token{}))
			var version string
			query := "SELECT version()"
			if kind == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("synthetic revocation database=%s version=%s", kind, version)
			t.Run("faults", TestTokenRevocationRejectsStaleRedisAuthorization)
			t.Run("overlap", TestTokenRevocationWithOverlappingCachedAuthentication)
		})
	}
}

func TestTokenRevocationDatabaseFailureNeverUsesCachedAuthority(t *testing.T) {
	useUserCacheMiniRedis(t)
	token := Token{UserId: 7, Key: "synthetic-db-failure-credential", Name: "db-failure", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}
	require.NoError(t, token.Insert())
	db := DB
	t.Cleanup(func() { require.NoError(t, db.Unscoped().Delete(&token).Error) })
	require.NoError(t, cacheSetTokenForTest(token))
	var logs bytes.Buffer
	previousDB, previousDebug := DB, common.DebugEnabled
	DB = DB.Session(&gorm.Session{Logger: newGormLogger(&logs)})
	common.DebugEnabled = true
	common.LogWriterMu.Lock()
	previousWriter := gin.DefaultWriter
	gin.DefaultWriter = &logs
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		DB, common.DebugEnabled = previousDB, previousDebug
		common.LogWriterMu.Lock()
		gin.DefaultWriter = previousWriter
		common.LogWriterMu.Unlock()
	})
	const callback = "synthetic:token_authorization_failure"
	require.NoError(t, DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		tx.AddError(fmt.Errorf("synthetic driver error containing %s", token.Key))
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })
	for _, enabled := range []bool{true, false} {
		common.RedisEnabled = enabled
		_, err := ValidateUserToken(token.Key)
		require.ErrorIs(t, err, ErrDatabase)
		assert.NotContains(t, err.Error(), token.Key)
	}
	assert.NotContains(t, logs.String(), token.Key)
}

func TestTokenRevocationStaleCacheCannotWidenPermissions(t *testing.T) {
	useUserCacheMiniRedis(t)
	token := Token{UserId: 7, Key: "synthetic-stale-permissions", Name: "stale-permissions", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true, RemainQuota: 100, Group: "auto", CrossGroupRetry: true, AutoGroups: `["default","vip"]`, AllowUserQuotaQuery: true}
	require.NoError(t, token.Insert())
	db := DB
	t.Cleanup(func() { require.NoError(t, db.Unscoped().Delete(&token).Error) })
	require.NoError(t, cacheSetTokenForTest(token))
	// A second writer bypasses this process's invalidation entirely. New reads
	// must still observe committed restrictions rather than the shared hash.
	require.NoError(t, DB.Model(&token).Updates(map[string]any{
		"unlimited_quota": false, "remain_quota": 20, "group": "restricted",
		"model_limits_enabled": true, "model_limits": "synthetic-model", "allow_ips": "192.0.2.1",
		"cross_group_retry": false, "auto_groups": "", "allow_user_quota_query": false,
	}).Error)
	loaded, err := ValidateUserToken(token.Key)
	require.NoError(t, err)
	assert.False(t, loaded.UnlimitedQuota)
	assert.Equal(t, 20, loaded.RemainQuota)
	assert.Equal(t, "restricted", loaded.Group)
	assert.True(t, loaded.ModelLimitsEnabled)
	assert.Equal(t, "synthetic-model", loaded.ModelLimits)
	require.NotNil(t, loaded.AllowIps)
	assert.Equal(t, "192.0.2.1", *loaded.AllowIps)
	assert.False(t, loaded.CrossGroupRetry)
	assert.Empty(t, loaded.AutoGroups)
	assert.False(t, loaded.AllowUserQuotaQuery)
	// Existing atomic reservations must not be reset to a larger DB balance.
	require.NoError(t, common.RDB.HSet(context.Background(), getTokenCacheKey(token.Key), "RemainQuota", 3).Err())
	loaded, err = ValidateUserToken(token.Key)
	require.NoError(t, err)
	assert.Equal(t, 3, loaded.RemainQuota)
}

func TestTokenAutoGroupsRoundTripThroughRedisHashCache(t *testing.T) {
	useUserCacheMiniRedis(t)
	token := Token{
		Id:         42,
		UserId:     7,
		Key:        "token-auto-groups-cache-key",
		Name:       "auto-cache",
		Group:      "auto",
		AutoGroups: `["vip","default"]`,
	}

	require.NoError(t, cacheSetTokenForTest(token))
	cached, err := cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.Equal(t, token.AutoGroups, cached.AutoGroups)
	groups, err := cached.GetAutoGroups()
	require.NoError(t, err)
	assert.Equal(t, []string{"vip", "default"}, groups)
}

func TestTokenAllowUserQuotaQueryRoundTripsThroughRedisHashCache(t *testing.T) {
	server := useUserCacheMiniRedis(t)
	token := Token{Id: 43, UserId: 7, Key: "token-user-quota-cache-key", Name: "user-quota-cache", AllowUserQuotaQuery: true}

	initialized, err := cacheInitToken(token)
	require.NoError(t, err)
	require.Equal(t, 1, initialized)
	cached, err := cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.True(t, cached.AllowUserQuotaQuery)
	assert.Positive(t, server.TTL(getTokenCacheKey(token.Key)))
}

func TestTokenUpdateSynchronouslyNarrowsPreheatedAutoGroupsCache(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	token := Token{
		UserId:          7,
		Key:             "token-auto-groups-update-cache-key",
		Name:            "auto-cache-update",
		Status:          common.TokenStatusEnabled,
		ExpiredTime:     -1,
		UnlimitedQuota:  true,
		Group:           "auto",
		CrossGroupRetry: true,
		AutoGroups:      `["default","vip"]`,
	}
	require.NoError(t, token.Insert())
	require.NoError(t, cacheSetTokenForTest(token))

	preheated, err := cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.JSONEq(t, `["default","vip"]`, preheated.AutoGroups)

	require.NoError(t, token.SetAutoGroups([]string{"vip"}))
	require.NoError(t, token.Update())
	// Update 是限制性变更：写库前删除缓存并设置 fence。缓存不再提供旧的
	// 宽分组值，下一次读取必须看到收紧后的分组。
	_, cacheErr := cacheGetTokenByKey(token.Key)
	require.Error(t, cacheErr, "the pre-update cache entry must be invalidated")
	reloaded, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.JSONEq(t, `["vip"]`, reloaded.AutoGroups)
}

// cacheSetTokenForTest 以测试身份写入完整 token 缓存（含额度字段），
// 模拟“已水合”的缓存状态。
func cacheSetTokenForTest(token Token) error {
	_, err := cacheInitToken(token)
	return err
}
