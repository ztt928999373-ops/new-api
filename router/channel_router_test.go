package router

import (
	"context"
	"errors"
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

type denySupplierTransport struct{ t *testing.T }

func (d denySupplierTransport) RoundTrip(*http.Request) (*http.Response, error) {
	d.t.Error("unexpected supplier request: outbound transport is blocked")
	return nil, errors.New("synthetic outbound denied")
}

func TestCredentialRoutesAuthenticateAuthorizeAndRejectUnsupported(t *testing.T) {
	// Keep the real revocation path, but make any accidental provider request fail locally.
	if client := service.GetHttpClient(); client != nil {
		original := client.Transport
		client.Transport = denySupplierTransport{t}
		t.Cleanup(func() { client.Transport = original })
	}

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
	t.Run("F1 role matrix", func(t *testing.T) {
		require.NoError(t, db.AutoMigrate(&model.Model{}, &model.Vendor{}, &model.Log{}, &model.QuotaData{}))
		require.NoError(t, db.Create(&model.Log{UserId: 1, CreatedAt: common.GetTimestamp(), Content: "F1-other-user-only", Username: "F1-other-user-only"}).Error)
		require.NoError(t, db.Create(&model.QuotaData{UserID: 1, CreatedAt: common.GetTimestamp(), Username: "F1-other-user-only"}).Error)
		for _, id := range []int{2, 3} {
			marker := fmt.Sprintf("F1-own-user-%d", id)
			require.NoError(t, db.Create(&model.Log{UserId: id, CreatedAt: common.GetTimestamp(), Content: marker, Username: marker}).Error)
			require.NoError(t, db.Create(&model.QuotaData{UserID: id, CreatedAt: common.GetTimestamp(), Username: marker}).Error)
		}
		api := gin.New()
		SetApiRouter(api)
		for _, tc := range []struct {
			method, path, body string
			allowed            int
		}{
			{"POST", "/api/channel/credentials", `{"key":"synthetic-F1-created"}`, 200},
			{"PUT", "/api/channel/credentials/" + platform.ID, `{"key":"synthetic-F1-stale","revision":99}`, 409},
			{"POST", "/api/channel/credentials/" + platform.ID + "/status", `{"status":"disabled","revision":99}`, 409},
		} {
			for _, role := range []int{0, common.RoleCommonUser, common.RoleAdminUser, common.RoleRootUser} {
				t.Run(fmt.Sprintf("%s %s/role=%d", tc.method, tc.path, role), func(t *testing.T) {
					req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
					req.Header.Set("Content-Type", "application/json")
					if role != 0 {
						req.Header.Set("Authorization", "Bearer "+tokens[role])
					}
					w := httptest.NewRecorder()
					api.ServeHTTP(w, req)
					want := http.StatusForbidden
					if role == 0 {
						want = http.StatusUnauthorized
					} else if role == common.RoleRootUser {
						want = tc.allowed
					}
					require.Equal(t, want, w.Code, w.Body.String())
					require.NotContains(t, w.Body.String(), "synthetic-F1-created")
				})
			}
		}
		for _, endpoint := range []string{"/api/channel/", "/api/channel/credentials/" + platform.ID, "/api/models/", "/api/log/", "/api/data/", "/api/log/self", "/api/data/self"} {
			for _, role := range []int{0, common.RoleCommonUser, common.RoleAdminUser, common.RoleRootUser} {
				t.Run(fmt.Sprintf("%s/role=%d", endpoint, role), func(t *testing.T) {
					requestPath := endpoint
					if strings.HasPrefix(endpoint, "/api/data/") || strings.HasPrefix(endpoint, "/api/log/") {
						requestPath += fmt.Sprintf("?start_timestamp=%d&end_timestamp=%d", common.GetTimestamp()-3600, common.GetTimestamp()+3600)
					}
					req := httptest.NewRequest(http.MethodGet, requestPath, nil)
					if role != 0 {
						req.Header.Set("Authorization", "Bearer "+tokens[role])
					}
					w := httptest.NewRecorder()
					api.ServeHTTP(w, req)
					want := http.StatusOK
					if role == 0 {
						want = http.StatusUnauthorized
					} else if role == common.RoleCommonUser && !strings.HasSuffix(endpoint, "/self") {
						want = http.StatusForbidden
					}
					require.Equal(t, want, w.Code, w.Body.String())
					var payload struct {
						Success bool `json:"success"`
					}
					require.NoError(t, common.Unmarshal(w.Body.Bytes(), &payload))
					require.Equal(t, want == http.StatusOK, payload.Success, w.Body.String())
					if role != common.RoleRootUser && strings.HasSuffix(endpoint, "/self") {
						if role != 0 {
							ownID := 3
							if role == common.RoleAdminUser {
								ownID = 2
							}
							require.Contains(t, w.Body.String(), fmt.Sprintf("F1-own-user-%d", ownID))
							otherID := 2
							if ownID == 2 {
								otherID = 3
							}
							require.NotContains(t, w.Body.String(), fmt.Sprintf("F1-own-user-%d", otherID))
						}
						require.NotContains(t, w.Body.String(), "F1-other-user-only")
					}
				})
			}
		}
	})
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
		{"managed metadata", http.MethodGet, fmt.Sprintf("/api/channel/credentials/channel/%d", managed.Id), "", common.RoleRootUser, 200},
		{"legacy metadata", http.MethodGet, fmt.Sprintf("/api/channel/credentials/channel/%d", legacy.Id), "", common.RoleRootUser, 200},
		{"metadata requires permission", http.MethodGet, fmt.Sprintf("/api/channel/credentials/channel/%d", managed.Id), "", common.RoleCommonUser, 403},
		{"managed test rejected", http.MethodGet, fmt.Sprintf("/api/channel/test/%d", managed.Id), "", common.RoleRootUser, 503},
		{"managed edit rejected", http.MethodPut, "/api/channel/", fmt.Sprintf(`{"id":%d,"key":"synthetic-new"}`, managed.Id), common.RoleRootUser, 503},
		{"legacy disable", http.MethodPost, fmt.Sprintf("/api/channel/%d/status", legacy.Id), `{"status":2}`, common.RoleRootUser, 200},
		{"legacy reenable", http.MethodPost, fmt.Sprintf("/api/channel/%d/status", legacy.Id), `{"status":1}`, common.RoleRootUser, 200},
		{"managed disable", http.MethodPost, fmt.Sprintf("/api/channel/%d/status", managed.Id), `{"status":2}`, common.RoleRootUser, 200},
		{"managed reenable", http.MethodPost, fmt.Sprintf("/api/channel/%d/status", managed.Id), `{"status":1}`, common.RoleRootUser, 200},
		{"managed idempotent", http.MethodPost, fmt.Sprintf("/api/channel/%d/status", managed.Id), `{"status":1}`, common.RoleRootUser, 200},
		{"invalid missing channel", http.MethodPost, "/api/channel/999999/status", `{"status":1}`, common.RoleRootUser, 200},
		{"mixed batch status", http.MethodPost, "/api/channel/status/batch", fmt.Sprintf(`{"ids":[%d,%d],"status":1}`, managed.Id, legacy.Id), common.RoleRootUser, 200},
		{"invalid batch rollback", http.MethodPost, "/api/channel/status/batch", fmt.Sprintf(`{"ids":[%d,999999],"status":2}`, managed.Id), common.RoleRootUser, 200},
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
			if tc.method == http.MethodPost && strings.Contains(tc.path, "/status") {
				if strings.HasPrefix(tc.name, "invalid") {
					require.Contains(t, w.Body.String(), `"success":false`)
				} else {
					require.Contains(t, w.Body.String(), `"success":true`)
					var input struct {
						Status int   `json:"status"`
						Ids    []int `json:"ids"`
					}
					require.NoError(t, common.UnmarshalJsonStr(tc.body, &input))
					if len(input.Ids) == 0 {
						var id int
						_, err := fmt.Sscanf(tc.path, "/api/channel/%d/status", &id)
						require.NoError(t, err)
						input.Ids = []int{id}
					}
					for _, id := range input.Ids {
						var stored model.Channel
						require.NoError(t, db.First(&stored, id).Error)
						require.Equal(t, input.Status, stored.Status)
					}
				}
			}
			if tc.status == 503 && strings.Contains(tc.name, "managed") {
				require.Contains(t, w.Body.String(), "MANAGED_CREDENTIAL_OPERATION_UNSUPPORTED")
			}
		})
	}
	var copies int64
	var after model.Channel
	require.NoError(t, db.First(&after, managed.Id).Error)
	require.Equal(t, common.ChannelStatusEnabled, after.Status, "failed batch must not persist its preceding update")
	require.NoError(t, db.Model(&model.Channel{}).Where("credential_ref = ?", platform.ID).Count(&copies).Error)
	require.EqualValues(t, 1, copies, "rejected copy must not duplicate a credential binding")
	t.Run("F1 revision and irreversible revocation", func(t *testing.T) {
		for _, tc := range []struct {
			name, method, suffix, body string
			status                     int
		}{
			{"stale revision", "PUT", "", `{"key":"synthetic-rejected-secret","revision":99}`, 409},
			{"revoke", "POST", "/status", `{"status":"revoked","revision":1}`, 200},
			{"reenable revoked", "POST", "/status", `{"status":"active","revision":2}`, 503},
			{"replace revoked", "PUT", "", `{"key":"synthetic-rejected-secret","revision":2}`, 503},
			{"use revoked", "POST", "/test", `{}`, 503},
		} {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest(tc.method, "/api/channel/credentials/"+platform.ID+tc.suffix, strings.NewReader(tc.body))
				req.Header.Set("Authorization", "Bearer "+tokens[common.RoleRootUser])
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				engine.ServeHTTP(w, req)
				require.Equal(t, tc.status, w.Code, w.Body.String())
				require.NotContains(t, w.Body.String(), "synthetic-rejected-secret")
				if tc.status == 409 {
					require.Contains(t, w.Body.String(), "CREDENTIAL_REVISION_CONFLICT")
				}
			})
		}
		head, err := s.Metadata(context.Background(), credentials.Principal{TenantID: "default", UserID: "1", PlatformOperator: true}, platform.ID)
		require.NoError(t, err)
		require.Equal(t, "revoked", head.Status)
		require.EqualValues(t, 2, head.Revision)
		var events []credentials.AccessEvent
		require.NoError(t, db.Find(&events).Error)
		data, err := common.Marshal(events)
		require.NoError(t, err)
		require.NotContains(t, string(data), "synthetic-platform-secret")
		require.NotContains(t, string(data), "synthetic-rejected-secret")
	})
	t.Run("F1 revoked dashboard session is 401", func(t *testing.T) {
		bundle, err := service.CreateLoginSession(1, "synthetic", "127.0.0.1", "F1")
		require.NoError(t, err)
		require.NoError(t, service.RevokeByRefreshToken(bundle.RefreshToken, bundle.Session.SID, "F1 synthetic logout"))
		req := httptest.NewRequest("GET", "/api/channel/credentials/"+platform.ID, nil)
		req.Header.Set("Authorization", "Bearer "+bundle.AccessToken)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		require.Equal(t, 401, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), "AUTH_SESSION_REVOKED")
	})
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
	assertChannelRoutePermission(t, http.MethodPost, "/bailian-integration", authz.ChannelSensitiveWrite, controller.PrepareBailianIntegration)
	assertChannelRoutePermission(t, http.MethodPost, "/openai-integration", authz.ChannelSensitiveWrite, controller.PrepareOpenAIIntegration)
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

func TestBailianPreparationRequiresAuthentication(t *testing.T) {
	engine := gin.New()
	registerChannelRoutes(engine.Group("/api"))
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/channel/bailian-integration", nil))
	require.Equal(t, http.StatusUnauthorized, w.Code)
}
func TestOpenAIPreparationRequiresAuthentication(t *testing.T) {
	engine := gin.New()
	registerChannelRoutes(engine.Group("/api"))
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/channel/openai-integration", nil))
	require.Equal(t, http.StatusUnauthorized, w.Code)
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

func TestF1DisabledSelectorsAndRetries(t *testing.T) {
	setupRelayRouterTestDB(t)
	db := model.DB
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	managed := model.Channel{Name: "F1-only", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI}
	require.NoError(t, db.Create(&managed).Error)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", managed.Id).Updates(map[string]any{"models": "F1-only", "group": "F1-only"}).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: managed.Id, Group: "F1-only", Model: "F1-only", Enabled: true}).Error)
	defer func() { common.MemoryCacheEnabled = false }()
	for _, memory := range []bool{false, true} {
		common.MemoryCacheEnabled = memory
		_, err := model.SetChannelOverallStatus([]int{managed.Id}, common.ChannelStatusEnabled, "F1 synthetic")
		require.NoError(t, err)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		param := &service.RetryParam{Ctx: c, TokenGroup: "F1-only", ModelName: "F1-only"}
		selected, _, err := service.CacheGetRandomSatisfiedChannel(param)
		require.NoError(t, err)
		require.NotNil(t, selected)
		_, err = model.SetChannelOverallStatus([]int{managed.Id}, common.ChannelStatusManuallyDisabled, "F1 synthetic")
		require.NoError(t, err)
		require.NoError(t, model.InitChannelCache())
		for retry := range 3 {
			param.SetRetry(retry)
			selected, _, _ = service.CacheGetRandomSatisfiedChannel(param)
			require.Nil(t, selected, "disabled channel selected with cache=%v retry=%d", memory, retry)
		}
	}
}
