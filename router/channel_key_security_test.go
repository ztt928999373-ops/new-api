// Copyright (c) QuantumNous and contributors.
package router

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/service/credentials"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelKeySecurityDatabaseMatrix(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			db := setupUpstreamBudgetDB(t, kind)
			require.NoError(t, db.AutoMigrate(&model.Ability{}, &model.User{}, &model.UserSession{}, &model.UserAccessToken{}, &model.AuditLog{}, &model.CasbinRule{}, &model.AuthzRole{}))
			if kind == "postgres" {
				// Initialize host column quoting as real PostgreSQL startup does.
				// setupUpstreamBudgetDB replaces a SQLite fixture's connection, so
				// changing only its dialect flags leaves the old quoted key name.
				t.Setenv("SQL_DSN", "postgres://postgres:synthetic-test-only@127.0.0.1:25436/credential_test?sslmode=disable")
				require.NoError(t, model.OpenPreparedDB())
				connection, err := model.DB.DB()
				require.NoError(t, err)
				require.NoError(t, connection.Close())
				model.DB = db
			}
			require.NoError(t, authz.Init(db))
			oldMaster, oldSecret, oldMemory := common.IsMasterNode, common.SessionSecret, common.MemoryCacheEnabled
			common.IsMasterNode, common.SessionSecret, common.MemoryCacheEnabled = true, "synthetic-key-security-session", true
			t.Cleanup(func() {
				common.IsMasterNode, common.SessionSecret, common.MemoryCacheEnabled = oldMaster, oldSecret, oldMemory
			})
			tokens := map[int]string{}
			for _, role := range []int{1, 10, 100} {
				u := model.User{Username: fmt.Sprintf("key-security-%d", role), Role: role, Status: 1, AuthVersion: 1, Group: "default", AffCode: fmt.Sprintf("keys-%d", role)}
				require.NoError(t, db.Create(&u).Error)
				t.Cleanup(func() { db.Delete(&u) })
				login, err := service.CreateLoginSession(u.Id, "synthetic", "127.0.0.1", "synthetic")
				require.NoError(t, err)
				tokens[role] = login.AccessToken
			}
			installBudgetTestCredential(t, db, 123)
			api := gin.New()
			registerChannelRoutes(api.Group("/api"))
			request := func(role int, method, path string, body any) *httptest.ResponseRecorder {
				data, err := common.Marshal(body)
				require.NoError(t, err)
				r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
				r.Header.Set("Content-Type", "application/json")
				if role != 0 {
					r.Header.Set("Authorization", "Bearer "+tokens[role])
				}
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				assert.NotContains(t, w.Body.String(), "synthetic-slot-one")
				assert.NotContains(t, w.Body.String(), "synthetic-slot-two")
				assert.NotContains(t, w.Body.String(), "synthetic-rotated-one")
				assert.NotContains(t, w.Body.String(), "synthetic-rotated-two")
				return w
			}
			input := map[string]any{"mode": "multi_to_single", "multi_key_mode": "polling", "channel": map[string]any{"name": "key-security-channel", "type": 1, "key": "synthetic-slot-one\nsynthetic-slot-two", "models": "synthetic-model", "group": "default", "status": 1}}
			for _, role := range []int{0, 1, 10} {
				w := request(role, "POST", "/api/channel/", input)
				want := 403
				if role == 0 {
					want = 401
				}
				assert.Equal(t, want, w.Code)
			}
			w := request(100, "POST", "/api/channel/", input)
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), `"success":true`)
			var ch model.Channel
			require.NoError(t, db.Where("name = ?", "key-security-channel").First(&ch).Error)
			t.Cleanup(func() {
				db.Where("channel_id = ?", ch.Id).Delete(&model.Ability{})
				db.Delete(&ch)
				model.InitChannelCache()
			})
			assert.Empty(t, ch.Key)
			assert.NotEmpty(t, ch.CredentialRef)
			assert.Equal(t, common.ChannelStatusManuallyDisabled, ch.Status)
			assert.Equal(t, 2, ch.ChannelInfo.MultiKeySize)
			for _, mode := range []string{"single", "batch"} {
				setting := `{"force_format":false,"thinking_to_content":false,"proxy":"","pass_through_body_enabled":false,"responses_websocket_enabled":false,"system_prompt":"","system_prompt_override":false}`
				settings := `{"allow_service_tier":false,"disable_store":false,"allow_safety_identifier":false,"allow_include_obfuscation":false,"allow_inference_geo":false,"disable_task_polling_sleep":false,"upstream_model_update_check_enabled":false,"upstream_model_update_auto_sync_enabled":false,"upstream_model_update_ignored_models":[],"upstream_model_update_last_detected_models":[],"upstream_model_update_last_check_time":0}`
				key := "synthetic-create-one"
				want := 1
				if mode == "batch" {
					key += "\nsynthetic-create-two"
					want = 2
				}
				body := map[string]any{"mode": mode, "channel": map[string]any{"name": "key-security-" + mode, "type": 1, "key": key, "models": "synthetic-other", "group": "default", "setting": setting, "settings": settings}}
				w := request(100, "POST", "/api/channel/", body)
				require.Equal(t, 200, w.Code, w.Body.String())
				require.Contains(t, w.Body.String(), `"success":true`)
				var created []model.Channel
				require.NoError(t, db.Where("name = ?", "key-security-"+mode).Find(&created).Error)
				require.Len(t, created, want)
				for _, item := range created {
					assert.Empty(t, item.Key)
					assert.NotEmpty(t, item.CredentialRef)
					assert.Equal(t, common.ChannelStatusManuallyDisabled, item.Status)
					t.Cleanup(func() { db.Where("channel_id = ?", item.Id).Delete(&model.Ability{}); db.Delete(&item) })
				}
			}
			for _, target := range []string{"http://example.invalid", "https://user:synthetic-password@example.invalid", "https://example.invalid?key=synthetic-query"} {
				w := request(100, "POST", "/api/channel/", map[string]any{"mode": "single", "channel": map[string]any{"name": "rejected-target", "type": 1, "key": "synthetic-only", "models": "synthetic-model", "base_url": target}})
				assert.Equal(t, 503, w.Code)
			}
			input["batch_add_set_key_prefix_2_name"] = true
			w = request(100, "POST", "/api/channel/", input)
			assert.Equal(t, 503, w.Code, "secret prefix naming must fail before persistence")
			delete(input, "batch_add_set_key_prefix_2_name")
			previousResolver := service.SetChannelCredentialResolver(nil)
			w = request(100, "POST", "/api/channel/", input)
			assert.Equal(t, 503, w.Code, "missing vault never falls back to plaintext")
			service.SetChannelCredentialResolver(previousResolver)
			for _, path := range []string{"", fmt.Sprint(ch.Id), "credentials/channel/" + fmt.Sprint(ch.Id)} {
				w := request(100, "GET", "/api/channel/"+path, nil)
				assert.Equal(t, 200, w.Code)
			}
			for _, path := range []string{fmt.Sprint(ch.Id) + "/key", "copy/" + fmt.Sprint(ch.Id)} {
				w := request(100, "POST", "/api/channel/"+path, map[string]any{})
				assert.NotContains(t, w.Body.String(), `"success":true`)
			}
			// The existing revision-fenced management endpoint rotates the whole
			// bundle; slot count and a revoked head cannot be bypassed.
			credentialPath := "/api/channel/credentials/" + ch.CredentialRef
			w = request(100, "PUT", credentialPath, map[string]any{"revision": 1, "key": "synthetic-wrong-slot-count"})
			assert.Equal(t, 503, w.Code)
			w = request(100, "PUT", credentialPath, map[string]any{"revision": 1, "key": "synthetic-rotated-one\nsynthetic-rotated-two"})
			require.Equal(t, 200, w.Code, w.Body.String())
			w = request(100, "PUT", credentialPath, map[string]any{"revision": 1, "key": "synthetic-slot-one\nsynthetic-slot-two"})
			assert.Equal(t, 409, w.Code)
			_, err := model.SetChannelOverallStatus([]int{ch.Id}, common.ChannelStatusEnabled, "synthetic test")
			require.NoError(t, err)
			for i, want := range []string{"synthetic-rotated-one", "synthetic-rotated-two", "synthetic-rotated-one"} {
				cached, err := model.CacheGetChannel(ch.Id)
				require.NoError(t, err)
				assert.Empty(t, cached.Key)
				assert.Empty(t, cached.Keys)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
				resolved, err := service.BindChannelCredentialRequest(c, cached, 123)
				require.NoError(t, err)
				key, index, apiErr := resolved.GetNextEnabledKey()
				require.Nil(t, apiErr)
				assert.Equal(t, want, key)
				assert.Equal(t, i%2, index)
				service.ReleaseChannelCredentialRequest(c)
			}
			// Exercise the actual token-authenticated relay and a loopback HTTP
			// server, not just resolver helpers. No external target is contacted.
			var calls atomic.Int64
			var fail atomic.Bool
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Contains(t, []string{"Bearer synthetic-rotated-one", "Bearer synthetic-rotated-two", "Bearer synthetic-create-one"}, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				if fail.Load() && strings.Contains(r.Header.Get("Authorization"), "synthetic-rotated-") {
					key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
					w.WriteHeader(500)
					data, _ := common.Marshal(map[string]any{"error": map[string]any{"message": "mock echoed " + key, "type": "upstream_error", "code": key, "metadata": map[string]any{"key": key}}})
					_, _ = w.Write(data)
					return
				}
				_, _ = w.Write([]byte(`{"id":"synthetic","object":"chat.completion","model":"synthetic-model","choices":[{"index":0,"message":{"role":"assistant","content":"mock"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))
			}))
			t.Cleanup(mock.Close)
			require.NoError(t, db.Model(&ch).Update("base_url", mock.URL).Error)
			require.NoError(t, model.InitChannelCache())
			require.NoError(t, db.AutoMigrate(&model.Token{}, &model.Log{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}, &model.Option{}))
			client := model.User{Username: "synthetic-key-relay", Role: 1, Status: 1, Group: "default", Quota: 1000000, AffCode: "key-relay"}
			require.NoError(t, db.Create(&client).Error)
			t.Cleanup(func() { db.Delete(&client) })
			token := model.Token{UserId: client.Id, Key: "synthetickeyrelay", Status: 1, ExpiredTime: -1, RemainQuota: 1000000}
			require.NoError(t, db.Create(&token).Error)
			t.Cleanup(func() { db.Delete(&token) })
			previousPrices, previousBatch := ratio_setting.ModelPrice2JSONString(), common.BatchUpdateEnabled
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"synthetic-model":0.002}`))
			common.BatchUpdateEnabled = false
			t.Cleanup(func() {
				ratio_setting.UpdateModelPriceByJSONString(previousPrices)
				common.BatchUpdateEnabled = previousBatch
			})
			service.InitHttpClient()
			SetRelayRouter(api)
			t.Cleanup(func() {
				require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond)
			})
			relay := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"synthetic-model","messages":[{"role":"user","content":"mock"}],"max_tokens":8}`))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Authorization", "Bearer "+token.Key)
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				return w
			}
			for range 2 {
				w := relay()
				require.Equal(t, 200, w.Code, w.Body.String())
			}
			assert.EqualValues(t, 2, calls.Load())
			require.Error(t, model.DisableManagedChannelSlot(ch.Id, ch.CredentialRef, 1, 0), "an old credential failure cannot disable a replacement")
			require.NoError(t, model.DisableManagedChannelSlot(ch.Id, ch.CredentialRef, 2, 0))
			w = relay()
			require.Equal(t, 200, w.Code, w.Body.String())
			assert.EqualValues(t, 3, calls.Load())
			previousRetries, previousDisable := common.RetryTimes, common.AutomaticDisableChannelEnabled
			t.Cleanup(func() { common.RetryTimes, common.AutomaticDisableChannelEnabled = previousRetries, previousDisable })
			common.RetryTimes, common.AutomaticDisableChannelEnabled = 0, false
			fail.Store(true)
			w = relay()
			assert.Equal(t, 500, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), "synthetic-rotated-two")
			require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond)
			var logs []model.Log
			require.NoError(t, db.Where("channel_id = ?", ch.Id).Find(&logs).Error)
			for _, log := range logs {
				data, err := common.Marshal(log)
				require.NoError(t, err)
				assert.NotContains(t, string(data), "synthetic-rotated-two")
			}
			var fallback model.Channel
			require.NoError(t, db.Where("name = ?", "key-security-single").First(&fallback).Error)
			fallback.Models, fallback.Status, fallback.BaseURL = "synthetic-model", common.ChannelStatusEnabled, &mock.URL
			require.NoError(t, db.Model(&fallback).Updates(map[string]any{"models": fallback.Models, "status": fallback.Status, "base_url": mock.URL}).Error)
			require.NoError(t, fallback.UpdateAbilities(db))
			require.NoError(t, db.Model(&ch).Update("priority", 10).Error)
			require.NoError(t, model.InitChannelCache())
			common.RetryTimes = 1
			w = relay()
			require.Equal(t, 200, w.Code, w.Body.String())
			assert.EqualValues(t, 6, calls.Load(), "failed encrypted primary retries through the encrypted fallback")
			_, err = model.SetChannelOverallStatus([]int{fallback.Id}, common.ChannelStatusManuallyDisabled, "synthetic fallback done")
			require.NoError(t, err)
			common.RetryTimes, common.AutomaticDisableChannelEnabled = previousRetries, previousDisable
			fail.Store(false)
			_, err = model.SetChannelOverallStatus([]int{ch.Id}, common.ChannelStatusManuallyDisabled, "synthetic disable")
			require.NoError(t, err)
			w = relay()
			assert.Equal(t, 503, w.Code)
			assert.EqualValues(t, 6, calls.Load(), "disabled channel never reaches upstream")
			_, err = model.SetChannelOverallStatus([]int{ch.Id}, common.ChannelStatusEnabled, "synthetic enable")
			require.NoError(t, err)
			w = request(100, "POST", credentialPath+"/status", map[string]any{"revision": 2, "status": "revoked"})
			require.Equal(t, 200, w.Code)
			w = relay()
			assert.Equal(t, 503, w.Code, w.Body.String())
			assert.EqualValues(t, 6, calls.Load(), "revoked request never reaches the mock upstream")
			w = request(100, "PUT", credentialPath, map[string]any{"revision": 3, "key": "synthetic-slot-one\nsynthetic-slot-two"})
			assert.Equal(t, 503, w.Code)
			w = request(100, "POST", credentialPath+"/status", map[string]any{"revision": 3, "status": "active"})
			assert.Equal(t, 503, w.Code)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			_, err = service.BindChannelCredentialRequest(c, &ch, 123)
			assert.Error(t, err)
			// A failed encrypted write leaves no channel or plaintext fallback.
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("key-security-failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "supplier_credential_access_events" {
					tx.AddError(errors.New("synthetic database failure"))
				}
			}))
			w = request(100, "POST", "/api/channel/", input)
			assert.Equal(t, 503, w.Code)
			require.NoError(t, db.Callback().Create().Remove("key-security-failure"))
			var count int64
			require.NoError(t, db.Model(&model.Channel{}).Where("name = ?", ch.Name).Count(&count).Error)
			assert.EqualValues(t, 1, count)
			var versions []credentials.StoredVersion
			require.NoError(t, db.Where("credential_id = ?", ch.CredentialRef).Find(&versions).Error)
			for _, version := range versions {
				assert.NotContains(t, version.Envelope, "synthetic-slot-one")
			}
			assert.Equal(t, constant.ChannelTypeOpenAI, ch.Type)
		})
	}
}
