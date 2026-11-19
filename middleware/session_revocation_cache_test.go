package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionRevocationCacheFenceRejectsHTTPWhileDatabaseStillActive(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	server := miniredis.RunT(t)
	previousRDB := common.RDB
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RDB = previousRDB
	})

	user := createMiddlewarePATUser(t, "revoking-session-user", "unused-synthetic-pat")
	session := &model.UserSession{
		SID: "revoking-session-http", UserID: user.Id, Version: 1,
		UserAuthVersion: user.AuthVersion, Status: model.UserSessionStatusActive,
		RefreshHash: "synthetic-refresh-hash", LoginMethod: "password",
		LastActiveAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	require.NoError(t, model.CreateUserSession(session))
	accessToken, _, err := service.IssueAccessToken(service.AuthIdentity{
		UserID: user.Id, SessionID: session.SID,
		UserAuthVersion: session.UserAuthVersion, SessionVersion: session.Version,
	})
	require.NoError(t, err)

	cacheKey := "auth:session:" + common.GenerateHMACWithKey([]byte("user-session-cache-v1:"+common.SessionSecret), session.SID)
	// Model the interleaving after the deny write and before the database
	// transaction commits. The status change invalidates the active snapshot MAC.
	require.NoError(t, common.RDB.HSet(context.Background(), cacheKey, "Status", model.UserSessionStatusRevoking).Err())
	var authoritative model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", session.SID).First(&authoritative).Error)
	assert.Equal(t, model.UserSessionStatusActive, authoritative.Status)

	router := gin.New()
	router.GET("/protected", UserAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusUnauthorized, response.Code)
}
