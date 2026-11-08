package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/service/credentials"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestCredentialRoutesAuthenticateAuthorizeAndRejectUnsupported(t *testing.T) {
	oldType := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetMainDatabaseType(oldType) })
	oldDB, oldLogDB, oldRedis, oldSecret, oldMaster, oldMemory := model.DB, model.LOG_DB, common.RedisEnabled, common.SessionSecret, common.IsMasterNode, common.MemoryCacheEnabled
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "routes.db")), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.AuditLog{}, &model.UserAccessToken{}, &model.Option{}, &model.Channel{}, &model.Ability{}, &model.CasbinRule{}, &model.AuthzRole{}))
	model.DB, model.LOG_DB, common.RedisEnabled, common.SessionSecret, common.IsMasterNode, common.MemoryCacheEnabled = db, db, false, "synthetic-route-signing-secret", true, false
	t.Cleanup(func() {
		model.DB, model.LOG_DB, common.RedisEnabled, common.SessionSecret, common.IsMasterNode, common.MemoryCacheEnabled = oldDB, oldLogDB, oldRedis, oldSecret, oldMaster, oldMemory
	})
	require.NoError(t, authz.Init(db))
	dir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, credentialcrypto.InitializeLocalDirectory(dir))
	path := filepath.Join(dir, "ring")
	require.NoError(t, credentialcrypto.InitializeLocalFile(path))
	p, err := credentialcrypto.NewLocalFileKeyProvider(path)
	require.NoError(t, err)
	s := &credentials.Store{DB: db, Environment: "test", ProviderID: "local-test", Provider: p}
	require.NoError(t, s.MigrateSchema())
	previous := service.SetChannelCredentialResolver(&credentials.Resolver{Environment: "test", Repository: s, Providers: map[string]credentialcrypto.KeyProvider{"local-test": p}, Audit: s})
	t.Cleanup(func() { service.SetChannelCredentialResolver(previous) })
	tokens := make(map[int]string)
	for i, role := range []int{common.RoleRootUser, common.RoleAdminUser, common.RoleCommonUser} {
		u := model.User{Username: fmt.Sprintf("synthetic-route-user-%d", i), Password: "synthetic-placeholder", Role: role, Status: common.UserStatusEnabled, AuthVersion: 1, Group: "default", AffCode: fmt.Sprintf("synthetic-route-%d", i)}
		require.NoError(t, db.Create(&u).Error)
		bundle, err := service.CreateLoginSession(u.Id, "synthetic", "127.0.0.1", "synthetic-route-test")
		require.NoError(t, err)
		tokens[role] = bundle.AccessToken
	}
	platform, err := s.Create(context.Background(), credentials.Principal{TenantID: "default", UserID: "1", PlatformOperator: true}, true, "openai", "api_key", "relay", []byte("synthetic-platform-secret"))
	require.NoError(t, err)
	byok, err := s.Create(context.Background(), credentials.Principal{TenantID: "default", UserID: "2"}, false, "openai", "api_key", "relay", []byte("synthetic-byok-secret"))
	require.NoError(t, err)
	managed := model.Channel{Name: "synthetic-managed-route", Type: constant.ChannelTypeOpenAI, CredentialRef: platform.ID, Status: common.ChannelStatusEnabled, Group: "default"}
	require.NoError(t, db.Create(&managed).Error)
	legacy := model.Channel{Name: "synthetic-legacy-route", Type: constant.ChannelTypeOpenAI, Key: "synthetic-legacy-secret", Status: common.ChannelStatusEnabled, Group: "default"}
	require.NoError(t, db.Create(&legacy).Error)
	engine := gin.New()
	registerChannelRoutes(engine.Group("/api"))
	for _, tc := range []struct {
		name, method, path, body string
		role, status             int
	}{
		{"anonymous", http.MethodGet, "/api/channel/credentials/" + platform.ID, "", 0, 401},
		{"ordinary user", http.MethodGet, "/api/channel/credentials/" + platform.ID, "", common.RoleCommonUser, 403},
		{"admin sensitive denied", http.MethodPut, "/api/channel/credentials/" + platform.ID, `{"key":"synthetic-new","revision":1}`, common.RoleAdminUser, 403},
		{"admin metadata allowed", http.MethodGet, "/api/channel/credentials/" + platform.ID, "", common.RoleAdminUser, 200},
		{"root metadata allowed", http.MethodGet, "/api/channel/credentials/" + platform.ID, "", common.RoleRootUser, 200},
		{"root cannot read another BYOK", http.MethodGet, "/api/channel/credentials/" + byok.ID, "", common.RoleRootUser, 503},
		{"managed copy rejected", http.MethodPost, fmt.Sprintf("/api/channel/copy/%d", managed.Id), "", common.RoleRootUser, 503},
		{"legacy copy remains allowed", http.MethodPost, fmt.Sprintf("/api/channel/copy/%d", legacy.Id), "", common.RoleRootUser, 200},
		{"managed balance rejected", http.MethodGet, fmt.Sprintf("/api/channel/update_balance/%d", managed.Id), "", common.RoleRootUser, 503},
		{"managed test rejected", http.MethodGet, fmt.Sprintf("/api/channel/test/%d", managed.Id), "", common.RoleRootUser, 503},
		{"managed edit rejected", http.MethodPut, "/api/channel/", fmt.Sprintf(`{"id":%d,"key":"synthetic-new"}`, managed.Id), common.RoleRootUser, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if token := tokens[tc.role]; token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.NotContains(t, w.Body.String(), "synthetic-platform-secret")
			require.NotContains(t, w.Body.String(), "synthetic-byok-secret")
			if tc.status == 503 && strings.Contains(tc.name, "managed") {
				require.Contains(t, w.Body.String(), "MANAGED_CREDENTIAL_OPERATION_UNSUPPORTED")
			}
		})
	}
	var copies int64
	require.NoError(t, db.Model(&model.Channel{}).Where("credential_ref = ?", platform.ID).Count(&copies).Error)
	require.EqualValues(t, 1, copies, "rejected copy must not duplicate a credential binding")
}

func TestChannelDefaultBaseURLsRequireReadPermission(t *testing.T) {
	assertChannelRoutePermission(t, http.MethodGet, "/default_base_urls", authz.ChannelRead, controller.GetChannelDefaultBaseURLs)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	registerChannelRoutes(engine.Group("/api"))
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/channel/default_base_urls", nil))
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestChannelStatusRoutesUseExpectedPermissions(t *testing.T) {
	assertChannelRoutePermission(t, http.MethodGet, "/priority/order", authz.ChannelRead, controller.GetChannelPriorityOrder)
	assertChannelRoutePermission(t, http.MethodPost, "/priority/reorder", authz.ChannelWrite, controller.ReorderChannelPriority)
	assertChannelRoutePermission(t, http.MethodGet, "/:id/vllm/status", authz.ChannelRead, controller.GetVLLMChannelStatus)
	assertChannelRoutePermission(t, http.MethodGet, "/:id/sglang/status", authz.ChannelRead, controller.GetSGLangChannelStatus)
	assertChannelRoutePermission(t, http.MethodPost, "/:id/status", authz.ChannelOperate, controller.UpdateChannelStatus)
	assertChannelRoutePermission(t, http.MethodPost, "/status/batch", authz.ChannelOperate, controller.BatchUpdateChannelStatus)
	assertChannelRoutePermission(t, http.MethodPut, "/", authz.ChannelWrite, controller.UpdateChannel)
}

func TestChannelDeleteRoutesUseSensitiveWritePermission(t *testing.T) {
	assertChannelRoutePermission(t, http.MethodPost, "/credentials/:id/rewrap", authz.ChannelSensitiveWrite, controller.RewrapSupplierCredential)
	assertChannelRoutePermission(t, http.MethodPost, "/credentials", authz.ChannelSensitiveWrite, controller.CreateSupplierCredential)
	assertChannelRoutePermission(t, http.MethodPut, "/credentials/:id", authz.ChannelSensitiveWrite, controller.ReplaceSupplierCredential)
	assertChannelRoutePermission(t, http.MethodPost, "/credentials/:id/status", authz.ChannelSensitiveWrite, controller.SetSupplierCredentialStatus)
	assertChannelRoutePermission(t, http.MethodPost, "/credentials/:id/test", authz.ChannelOperate, controller.TestSupplierCredential)
	assertChannelRoutePermission(t, http.MethodDelete, "/:id", authz.ChannelSensitiveWrite, controller.DeleteChannel)
	assertChannelRoutePermission(t, http.MethodPost, "/batch", authz.ChannelSensitiveWrite, controller.DeleteChannelBatch)
	assertChannelRoutePermission(t, http.MethodDelete, "/disabled", authz.ChannelSensitiveWrite, controller.DeleteDisabledChannel)
	assertChannelRoutePermission(t, http.MethodPut, "/", authz.ChannelWrite, controller.UpdateChannel)
	assertChannelRoutePermission(t, http.MethodPut, "/tag", authz.ChannelWrite, controller.EditTagChannels)
	assertChannelRoutePermission(t, http.MethodPost, "/batch/tag", authz.ChannelWrite, controller.BatchSetChannelTag)
}

func TestChannelStatusRoutesRegisterWithoutConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api")

	require.NotPanics(t, func() {
		registerChannelRoutes(api)
	})
}

func assertChannelRoutePermission(t *testing.T, method string, path string, permission authz.Permission, handler any) {
	t.Helper()
	for _, route := range channelPermissionRoutes {
		if route.method == method && route.path == path {
			assert.Equal(t, permission, route.permission)
			assert.Equal(t, reflect.ValueOf(handler).Pointer(), reflect.ValueOf(route.handler).Pointer())
			return
		}
	}
	t.Fatalf("route %s %s not found", method, path)
}
