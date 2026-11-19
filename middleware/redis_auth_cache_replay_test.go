package middleware

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRedisAuthCacheTamperingCannotElevateDashboardPAT(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	server := miniredis.RunT(t)
	oldRedisEnabled, oldRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RedisEnabled, common.RDB = oldRedisEnabled, oldRDB
	})

	gin.SetMode(gin.TestMode)
	const pat = "isolated-redis-auth-replay"
	user := createMiddlewarePATUser(t, "redis-replay-ordinary-user", pat)
	require.Equal(t, common.RoleCommonUser, user.Role)
	require.NoError(t, model.DB.Model(user).Update("quota", 100).Error)
	require.NoError(t, model.DB.Create(&model.UserAccessToken{
		UserId:    user.Id,
		TokenHash: model.AccessTokenFingerprint(pat),
		TokenHint: "isolated",
	}).Error)
	_, err := model.GetUserCache(user.Id)
	require.NoError(t, err)

	called := 0
	router := gin.New()
	router.GET("/admin", AdminAuth(), func(c *gin.Context) {
		called++
		c.Status(http.StatusNoContent)
	})
	router.GET("/root", RootAuth(), func(c *gin.Context) {
		called++
		c.Status(http.StatusNoContent)
	})

	adminBefore := middlewareBearerRequest(router, "/admin", pat)
	rootBefore := middlewareBearerRequest(router, "/root", pat)
	require.Equal(t, http.StatusForbidden, adminBefore.Code)
	require.Equal(t, http.StatusForbidden, rootBefore.Code)
	require.Zero(t, called)

	cacheKey := fmt.Sprintf("user:%d", user.Id)
	require.NoError(t, common.RDB.HSet(context.Background(), cacheKey, "Role", common.RoleRootUser).Err())

	adminAfter := middlewareBearerRequest(router, "/admin", pat)
	rootAfter := middlewareBearerRequest(router, "/root", pat)
	var authoritative model.User
	require.NoError(t, model.DB.First(&authoritative, user.Id).Error)
	t.Logf("reproduction evidence: user_id=%d db_role=%d cached_role=%d admin_http=%d root_http=%d handler_calls=%d",
		user.Id, authoritative.Role, common.RoleRootUser, adminAfter.Code, rootAfter.Code, called)

	assert.Equal(t, common.RoleCommonUser, authoritative.Role)
	assert.Equal(t, http.StatusForbidden, adminAfter.Code)
	assert.Equal(t, http.StatusForbidden, rootAfter.Code)
	assert.Zero(t, called)

	cacheRole, err := common.RDB.HGet(context.Background(), cacheKey, "Role").Int()
	require.NoError(t, err)
	assert.Equal(t, common.RoleCommonUser, cacheRole, "tampered cache must be replaced from the authoritative database")
	reserved, err := model.TryReserveUserQuota(user.Id, 25)
	require.NoError(t, err)
	assert.True(t, reserved)
	remainingQuota, err := common.RDB.HGet(context.Background(), cacheKey, "Quota").Int()
	require.NoError(t, err)
	assert.Equal(t, 75, remainingQuota, "dynamic quota must retain atomic reservation semantics")

	disabled := createMiddlewarePATUser(t, "redis-replay-disabled-user", "disabled-cache-replay")
	require.NoError(t, model.DB.Model(disabled).Update("status", common.UserStatusDisabled).Error)
	_, err = model.GetUserCache(disabled.Id)
	require.NoError(t, err)
	disabledKey := fmt.Sprintf("user:%d", disabled.Id)
	require.NoError(t, common.RDB.HSet(context.Background(), disabledKey, "Status", common.UserStatusEnabled).Err())
	statusRouter := gin.New()
	statusRouter.GET("/user", UserAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	statusResponse := middlewareBearerRequest(statusRouter, "/user", "disabled-cache-replay")
	assert.Equal(t, http.StatusUnauthorized, statusResponse.Code, "tampering cannot reactivate a disabled user")
	var authoritativeDisabled model.User
	require.NoError(t, model.DB.First(&authoritativeDisabled, disabled.Id).Error)
	assert.Equal(t, common.UserStatusDisabled, authoritativeDisabled.Status)

	fields := map[string]string{
		"Group":    "admin",
		"Email":    "forged@example.invalid",
		"Username": "forged-admin",
		"Setting":  `{"language":"forged"}`,
	}
	for field, forged := range fields {
		require.NoError(t, common.RDB.HSet(context.Background(), cacheKey, field, forged).Err())
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		authoritative := map[string]string{
			"Group": user.Group, "Email": user.Email, "Username": user.Username, "Setting": user.Setting,
		}
		assert.Equal(t, authoritative[field], map[string]string{
			"Group": cached.Group, "Email": cached.Email, "Username": cached.Username, "Setting": cached.Setting,
		}[field], "field=%s", field)
	}
	require.NoError(t, common.RDB.HSet(context.Background(), cacheKey, "Role", "not-an-integer").Err())
	parsed, err := model.GetUserCache(user.Id)
	require.NoError(t, err, "malformed cache field must recover from the database")
	assert.Equal(t, common.RoleCommonUser, parsed.Role)
	expiresCacheKey := fmt.Sprintf("user:%d", user.Id)
	require.NoError(t, common.RDB.HSet(context.Background(), expiresCacheKey, "CacheExpiresAt", time.Now().Add(-time.Minute).Unix()).Err())
	parsed, err = model.GetUserCache(user.Id)
	require.NoError(t, err, "expired signed metadata must recover from the database")
	assert.Equal(t, common.RoleCommonUser, parsed.Role)

	other := createMiddlewarePATUser(t, "redis-replay-other-user", "other-cache-replay")
	_, err = model.GetUserCache(other.Id)
	require.NoError(t, err)
	otherKey := fmt.Sprintf("user:%d", other.Id)
	userHash, err := common.RDB.HGetAll(context.Background(), cacheKey).Result()
	require.NoError(t, err)
	require.NoError(t, common.RDB.Del(context.Background(), otherKey).Err())
	require.NoError(t, common.RDB.HSet(context.Background(), otherKey, userHash).Err())
	cachedOther, err := model.GetUserCache(other.Id)
	require.NoError(t, err)
	assert.Equal(t, other.Id, cachedOther.Id, "a cache entry copied across Redis keys must not authenticate")

	require.NoError(t, common.RDB.HDel(context.Background(), otherKey, "CacheMAC").Err())
	callbackName := "test:redis-auth-cache-db-failure"
	forcedDBFailure := fmt.Errorf("synthetic database unavailable")
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(forcedDBFailure)
		}
	}))
	_, dbErr := model.GetUserCache(other.Id)
	model.DB.Callback().Query().Remove(callbackName)
	assert.Error(t, dbErr, "invalid cache plus database read failure must fail closed")

	now := time.Now().Unix()
	session := &model.UserSession{
		SID: "redis-cache-session-tamper", UserID: user.Id, Version: 1, UserAuthVersion: 1,
		Status: model.UserSessionStatusActive, RefreshHash: "synthetic-refresh-hash", LoginMethod: "password",
		CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 300,
	}
	require.NoError(t, model.CreateUserSession(session))
	sessionCacheKey := "auth:session:" + common.GenerateHMACWithKey([]byte("user-session-cache-v1:"+common.SessionSecret), session.SID)
	require.NoError(t, common.RDB.HSet(context.Background(), sessionCacheKey,
		"UserID", other.Id,
	).Err())
	cachedSession, err := model.GetUserSessionCached(session.SID)
	require.NoError(t, err)
	assert.Equal(t, user.Id, cachedSession.UserID)
	assert.Equal(t, model.UserSessionStatusActive, cachedSession.Status)
}
