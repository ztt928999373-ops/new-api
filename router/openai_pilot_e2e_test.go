// Copyright (c) QuantumNous and contributors.
package router

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	urlpkg "net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/credentials"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIPilotAuthenticatedSettlement(t *testing.T)  { testPilotAuthenticatedSettlement(t, false) }
func TestBailianPilotAuthenticatedSettlement(t *testing.T) { testPilotAuthenticatedSettlement(t, true) }

type pilotTransport func(*http.Request) (*http.Response, error)

func (f pilotTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testPilotAuthenticatedSettlement(t *testing.T, bailian bool) {
	require.NoError(t, i18n.Init())
	for _, scenario := range []string{"ordinary", "replaced", "stream", "retry", "failure", "timeout", "interrupted", "insufficient", "invalid_token", "disabled", "revoked", "concurrent"} {
		t.Run(scenario, func(t *testing.T) {
			oldDB, oldLog := model.DB, model.LOG_DB
			t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLog })
			setupRelayRouterTestDB(t)
			// All request producers below finish before this deferred barrier.
			// gopool v0.1.3 decrements WorkerCount only after the task callback
			// returns and its queue is empty. Drain before t.Cleanup restores
			// RedisEnabled/DB/config that asynchronous metrics and refunds read.
			defer func() {
				require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond, "asynchronous relay work did not drain before fixture cleanup")
			}()
			require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Log{}, &model.Option{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}))
			cache, batch, consume := common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled
			retry, timeout, count, streamTimeout := common.RetryTimes, common.RelayTimeout, constant.CountToken, constant.StreamingTimeout
			common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = false, false, true
			common.RetryTimes, common.RelayTimeout, constant.CountToken, constant.StreamingTimeout = 1, 1, false, 1
			t.Cleanup(func() {
				common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = cache, batch, consume
				common.RetryTimes, common.RelayTimeout, constant.CountToken, constant.StreamingTimeout = retry, timeout, count, streamTimeout
				service.InitHttpClient()
			})
			service.InitHttpClient()
			prices := ratio_setting.ModelPrice2JSONString()
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"qwen3.7-flash-2026-07-15":0.002}`))
			t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(prices)) })
			initial := 1000000
			if scenario == "insufficient" {
				initial = 0
			}
			user := model.User{Username: "qwen3.7-flash-2026-07-15", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", Quota: initial}
			require.NoError(t, model.DB.Create(&user).Error)
			token := model.Token{UserId: user.Id, Key: "syntheticpilotclient", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000000}
			require.NoError(t, model.DB.Create(&token).Error)
			expectedKey := "syntheticpilotsupplier"
			if scenario == "replaced" {
				expectedKey = "syntheticreplacedsupplier"
			}
			var calls atomic.Int32
			release := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				n := calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer "+expectedKey {
					w.WriteHeader(401)
					return
				}
				if scenario == "timeout" {
					select {
					case <-r.Context().Done():
					case <-release:
					}
					return
				}
				if scenario == "failure" || (scenario == "retry" && n == 1) {
					w.WriteHeader(500)
					_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"synthetic failure echo syntheticpilotsupplier"}}`)
					return
				}
				if scenario == "stream" || scenario == "interrupted" {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"id\":\"mock\",\"model\":\"qwen3.7-flash-2026-07-15\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
					if scenario == "interrupted" {
						w.(http.Flusher).Flush()
						select {
						case <-r.Context().Done():
						case <-release:
						}
						return
					}
					_, _ = io.WriteString(w, "data: {\"id\":\"mock\",\"model\":\"qwen3.7-flash-2026-07-15\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":3,\"total_tokens\":14}}\n\ndata: [DONE]\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"mock","model":"qwen3.7-flash-2026-07-15","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`)
			}))
			defer upstream.Close()
			defer close(release)
			upstreamURL := upstream.URL
			url := upstreamURL
			dir := filepath.Join(t.TempDir(), "keys")
			require.NoError(t, credentialcrypto.InitializeLocalDirectory(dir))
			path := filepath.Join(dir, "ring")
			require.NoError(t, credentialcrypto.InitializeLocalFile(path))
			provider, err := credentialcrypto.NewLocalFileKeyProvider(path)
			require.NoError(t, err)
			store := &credentials.Store{DB: model.DB, Environment: "test", ProviderID: "local-test", Provider: provider}
			require.NoError(t, store.MigrateSchema())
			owner := credentials.Principal{TenantID: "default", UserID: "synthetic-admin", PlatformOperator: true}
			supplier := "openai"
			if bailian {
				supplier = "bailian"
			}
			head, err := store.Create(context.Background(), owner, true, supplier, "api_key", "relay", []byte("syntheticpilotsupplier"))
			require.NoError(t, err)
			previous := service.SetChannelCredentialResolver(&credentials.Resolver{Environment: "test", Repository: store, Providers: map[string]credentialcrypto.KeyProvider{"local-test": provider}, Audit: store})
			t.Cleanup(func() { service.SetChannelCredentialResolver(previous) })
			channel := model.Channel{Type: constant.ChannelTypeOpenAI, CredentialRef: head.ID, Name: "pilot", Status: common.ChannelStatusEnabled, BaseURL: &url, Models: "qwen3.7-flash-2026-07-15", Group: "default"}
			if bailian {
				channel.Type = constant.ChannelTypeAli
				official := "https://dashscope.aliyuncs.com"
				channel.BaseURL = &official
				target, err := urlpkg.Parse(upstreamURL)
				require.NoError(t, err)
				client := service.GetHttpClient()
				transport := client.Transport
				client.Transport = pilotTransport(func(req *http.Request) (*http.Response, error) {
					if req.URL.String() != "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions" {
						return nil, errors.New("unexpected synthetic upstream destination")
					}
					copied := req.Clone(req.Context())
					address := *req.URL
					address.Scheme, address.Host = target.Scheme, target.Host
					copied.URL = &address
					return transport.RoundTrip(copied)
				})
			}
			require.NoError(t, model.DB.Create(&channel).Error)
			require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "qwen3.7-flash-2026-07-15", ChannelId: channel.Id, Enabled: true}).Error)
			if scenario == "disabled" {
				_, err := model.SetChannelOverallStatus([]int{channel.Id}, common.ChannelStatusManuallyDisabled, "test")
				require.NoError(t, err)
			}
			if scenario == "replaced" {
				_, err := store.Replace(context.Background(), owner, head.ID, 1, []byte(expectedKey))
				require.NoError(t, err)
			}
			if scenario == "revoked" {
				require.NoError(t, store.SetStatus(context.Background(), owner, head.ID, 1, "revoked"))
			}
			engine := gin.New()
			engine.Use(middleware.RequestId())
			SetRelayRouter(engine)
			stream := scenario == "stream" || scenario == "interrupted"
			body := `{"model":"qwen3.7-flash-2026-07-15","messages":[{"role":"user","content":"hi"}],"max_tokens":16}`
			if stream {
				body = strings.TrimSuffix(body, "}") + `,"stream":true,"stream_options":{"include_usage":true}}`
			}
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer syntheticpilotclient")
			if scenario == "invalid_token" {
				r.Header.Set("Authorization", "Bearer invalid")
			}
			w := httptest.NewRecorder()
			if scenario == "concurrent" {
				responses := make(chan *httptest.ResponseRecorder, 4)
				requestCtx, cancelRequests := context.WithCancel(context.Background())
				pending := 4
				defer func() {
					cancelRequests()
					deadline := time.NewTimer(5 * time.Second)
					defer deadline.Stop()
					for pending > 0 {
						select {
						case <-responses:
							pending--
						case <-deadline.C:
							t.Error("concurrent request producers did not finish before fixture cleanup")
							return
						}
					}
				}()
				for range 4 {
					go func() {
						req := httptest.NewRequestWithContext(requestCtx, http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
						req.Header = r.Header.Clone()
						rec := httptest.NewRecorder()
						engine.ServeHTTP(rec, req)
						responses <- rec
					}()
				}
				for range 4 {
					select {
					case rec := <-responses:
						pending--
						require.Equal(t, 200, rec.Code, rec.Body.String())
						w = rec
					case <-time.After(5 * time.Second):
						t.Fatal("concurrent request did not finish")
					}
				}
			} else {
				engine.ServeHTTP(w, r)
			}
			if scenario == "failure" || scenario == "timeout" || (bailian && scenario == "retry") {
				require.Eventually(t, func() bool {
					var u model.User
					var tok model.Token
					return model.DB.First(&u, user.Id).Error == nil && model.DB.First(&tok, token.Id).Error == nil && u.Quota == initial && tok.RemainQuota == 1000000
				}, 3*time.Second, 10*time.Millisecond, "wait for production asynchronous refund")
			}
			var after model.User
			require.NoError(t, model.DB.First(&after, user.Id).Error)
			var logs []model.Log
			require.NoError(t, model.LOG_DB.Where("type = ?", model.LogTypeConsume).Find(&logs).Error)
			var afterToken model.Token
			require.NoError(t, model.DB.First(&afterToken, token.Id).Error)
			charged := 0
			for _, log := range logs {
				charged += log.Quota
				require.NotEmpty(t, log.RequestId)
				require.NotContains(t, log.Other+log.Content, "syntheticpilotsupplier")
				require.NotContains(t, log.Other+log.Content, expectedKey)
				require.NotContains(t, log.Other+log.Content, "syntheticpilotclient")
			}
			require.Equal(t, charged, initial-after.Quota, w.Body.String())
			require.Equal(t, charged, 1000000-afterToken.RemainQuota)
			expectedRequests := 1
			if scenario == "concurrent" {
				expectedRequests = 4
				require.Equal(t, int32(4), calls.Load())
				require.Len(t, logs, 4)
			}
			require.LessOrEqual(t, len(logs), expectedRequests, "retries and stream termination must not settle twice")
			require.NotContains(t, w.Body.String(), "syntheticpilotsupplier")
			require.NotContains(t, w.Body.String(), expectedKey)
			if scenario == "ordinary" || scenario == "replaced" || scenario == "stream" || (scenario == "retry" && !bailian) {
				require.Equal(t, 200, w.Code, w.Body.String())
				require.Len(t, logs, 1)
				require.Positive(t, charged)
				require.Equal(t, 11, logs[0].PromptTokens)
				require.Equal(t, 3, logs[0].CompletionTokens)
				require.Equal(t, channel.Id, logs[0].ChannelId)
				require.Contains(t, w.Body.String(), "hello")
			} else if scenario != "interrupted" && scenario != "concurrent" {
				require.GreaterOrEqual(t, w.Code, 400, w.Body.String())
				require.Zero(t, charged)
			}
			if scenario == "invalid_token" || scenario == "insufficient" || scenario == "disabled" || scenario == "revoked" {
				require.Zero(t, calls.Load())
			}
			if scenario == "retry" {
				if bailian {
					require.Equal(t, int32(1), calls.Load())
				} else {
					require.Equal(t, int32(2), calls.Load())
				}
			}
			if scenario == "interrupted" {
				require.Equal(t, int32(1), calls.Load(), "never replay a stream after output")
				require.Len(t, logs, 1)
				require.Contains(t, logs[0].Other, `"status":"error"`)
			}

		})
	}
}

// TestClosedCustomerLoop uses the production management and relay routes. Only
// the isolated fixture's registration window and synthetic quota grant differ
// from a closed deployment. No payment endpoint or external provider is used.
func TestClosedCustomerLoop(t *testing.T) {
	oldDB, oldLog := model.DB, model.LOG_DB
	t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLog })
	setupRelayRouterTestDB(t)
	require.NoError(t, i18n.Init())
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Log{}, &model.Option{}, &model.UserSession{}, &model.TwoFA{}, &model.PasskeyCredential{}, &model.AuthFlow{}, &model.UserOAuthBinding{}, &model.UserAccessToken{}, &model.AuditLog{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}))
	sessionSecret := common.SessionSecret
	common.SessionSecret = "synthetic-c1-session-only"
	t.Cleanup(func() { common.SessionSecret = sessionSecret })
	register, passwordRegister, login, encrypted, email, turnstile := common.RegisterEnabled, common.PasswordRegisterEnabled, common.PasswordLoginEnabled, common.PasswordLoginEncryptionEnabled, common.EmailVerificationEnabled, common.TurnstileCheckEnabled
	critical, global, memory, batch, consume := common.CriticalRateLimitEnable, common.GlobalApiRateLimitEnable, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled
	bonus, retry, count, defaultToken := common.QuotaForNewUser, common.RetryTimes, constant.CountToken, constant.GenerateDefaultToken
	t.Cleanup(func() {
		common.RegisterEnabled, common.PasswordRegisterEnabled, common.PasswordLoginEnabled, common.PasswordLoginEncryptionEnabled, common.EmailVerificationEnabled, common.TurnstileCheckEnabled = register, passwordRegister, login, encrypted, email, turnstile
		common.CriticalRateLimitEnable, common.GlobalApiRateLimitEnable, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = critical, global, memory, batch, consume
		common.QuotaForNewUser, common.RetryTimes, constant.CountToken, constant.GenerateDefaultToken = bonus, retry, count, defaultToken
		service.InitHttpClient()
	})
	common.RegisterEnabled, common.PasswordRegisterEnabled, common.PasswordLoginEnabled, common.PasswordLoginEncryptionEnabled = true, true, true, true
	common.EmailVerificationEnabled, common.TurnstileCheckEnabled, common.CriticalRateLimitEnable, common.GlobalApiRateLimitEnable = false, false, false, false
	common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = false, false, true
	common.QuotaForNewUser, common.RetryTimes, constant.CountToken, constant.GenerateDefaultToken = 0, 0, false, false
	defer func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond)
	}()
	// A synthetic fixed-price model is confined to this fixture and restored.
	prices := ratio_setting.ModelPrice2JSONString()
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"qwen3.7-flash-2026-07-15":0.002}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(prices)) })
	service.InitHttpClient()
	var fail atomic.Bool
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		if fail.Load() {
			w.WriteHeader(500)
			_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"synthetic failure"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"synthetic","model":"qwen3.7-flash-2026-07-15","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`)
	}))
	defer upstream.Close()
	// Reuse the encrypted credential repository rather than a plaintext channel.
	dir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, credentialcrypto.InitializeLocalDirectory(dir))
	path := filepath.Join(dir, "ring")
	require.NoError(t, credentialcrypto.InitializeLocalFile(path))
	provider, err := credentialcrypto.NewLocalFileKeyProvider(path)
	require.NoError(t, err)
	store := &credentials.Store{DB: model.DB, Environment: "test", ProviderID: "local-test", Provider: provider}
	require.NoError(t, store.MigrateSchema())
	head, err := store.Create(context.Background(), credentials.Principal{TenantID: "default", UserID: "synthetic-admin", PlatformOperator: true}, true, "openai", "api_key", "relay", []byte("synthetic-c1-supplier"))
	require.NoError(t, err)
	previous := service.SetChannelCredentialResolver(&credentials.Resolver{Environment: "test", Repository: store, Providers: map[string]credentialcrypto.KeyProvider{"local-test": provider}, Audit: store})
	t.Cleanup(func() { service.SetChannelCredentialResolver(previous) })
	channel := model.Channel{Type: constant.ChannelTypeOpenAI, CredentialRef: head.ID, Name: "synthetic-c1", Status: 1, BaseURL: &upstream.URL, Models: "qwen3.7-flash-2026-07-15", Group: "default"}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, channel.AddAbilities(nil))
	engine := gin.New()
	engine.Use(middleware.RequestId())
	SetApiRouter(engine)
	SetRelayRouter(engine)
	// This helper sends actual route requests; it never injects user identity.
	api := func(method, path, access string, body any) map[string]any {
		raw, err := common.Marshal(body)
		require.NoError(t, err)
		req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		if access != "" {
			req.Header.Set("Authorization", "Bearer "+access)
		}
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		require.Equal(t, 200, rec.Code, "path=%s", path)
		var result map[string]any
		require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &result))
		return result
	}
	type customer struct {
		user        model.User
		access, key string
		tokenID     int
	}
	customers := make([]customer, 2)
	for i := range customers {
		name := fmt.Sprintf("c1-synthetic-%d", i)
		result := api("POST", "/api/user/register", "", map[string]any{"username": name, "password": "Synthetic-C1-Only!2026"})
		require.Equal(t, true, result["success"], "registration failed")
		require.NoError(t, model.DB.Where("username = ?", name).First(&customers[i].user).Error)
		require.Zero(t, customers[i].user.Quota)
		require.Equal(t, common.RoleCommonUser, customers[i].user.Role)
		// Keep password encryption enabled: initialize only the synthetic test process key.
		if kid, _ := common.PasswordEncryptionPublicKey(); kid == "" {
			private, err := common.GeneratePasswordEncryptionPrivateKey()
			require.NoError(t, err)
			require.NoError(t, common.LoadPasswordEncryptionPrivateKey(private))
		}
		enc := api("GET", "/api/user/login/encryption-key", "", nil)["data"].(map[string]any)
		require.Equal(t, true, enc["enabled"])
		block, _ := pem.Decode([]byte(enc["public_key"].(string)))
		require.NotNil(t, block)
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		require.NoError(t, err)
		ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, parsed.(*rsa.PublicKey), []byte("Synthetic-C1-Only!2026"), nil)
		require.NoError(t, err)
		result = api("POST", "/api/user/login", "", map[string]any{"username": name, "password_encrypted": base64.StdEncoding.EncodeToString(ciphertext), "encryption_key_id": enc["kid"]})
		require.Equal(t, true, result["success"], "encrypted login failed")
		customers[i].access = result["data"].(map[string]any)["access_token"].(string)
		self := api("GET", "/api/user/self", customers[i].access, nil)
		require.Equal(t, true, self["success"])
		require.EqualValues(t, 0, self["data"].(map[string]any)["quota"])
		require.Equal(t, true, api("POST", "/api/token/", customers[i].access, map[string]any{"name": "synthetic-c1-client", "remain_quota": 1000000, "expired_time": -1})["success"])
		var token model.Token
		require.NoError(t, model.DB.Where("user_id = ?", customers[i].user.Id).First(&token).Error)
		customers[i].tokenID = token.Id
		result = api("POST", fmt.Sprintf("/api/token/%d/key", token.Id), customers[i].access, nil)
		require.Equal(t, true, result["success"])
		customers[i].key = result["data"].(map[string]any)["key"].(string)
		t.Logf("customer=%d registration/encrypted-login/zero-wallet/key-create passed", i)
	}
	invoke := func(i int) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"qwen3.7-flash-2026-07-15","messages":[{"role":"user","content":"synthetic"}],"max_tokens":16}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+customers[i].key)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec
	}
	require.GreaterOrEqual(t, invoke(0).Code, 400)
	require.Zero(t, calls.Load())
	// Explicit synthetic grant through the existing quota service, no payment.
	require.NoError(t, model.IncreaseUserQuota(customers[0].user.Id, 1000000, true))
	require.EqualValues(t, 1000000, api("GET", "/api/user/self", customers[0].access, nil)["data"].(map[string]any)["quota"])
	success := invoke(0)
	require.Equal(t, 200, success.Code)
	require.Contains(t, success.Body.String(), "hello")
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond)
	var after model.User
	require.NoError(t, model.DB.First(&after, customers[0].user.Id).Error)
	require.Equal(t, 999000, after.Quota)
	require.Equal(t, 1000, after.UsedQuota)
	var token model.Token
	require.NoError(t, model.DB.First(&token, customers[0].tokenID).Error)
	require.Equal(t, 999000, token.RemainQuota)
	fail.Store(true)
	require.GreaterOrEqual(t, invoke(0).Code, 400)
	require.Eventually(t, func() bool {
		var u model.User
		return model.DB.First(&u, after.Id).Error == nil && u.Quota == 999000 && gopool.WorkerCount() == 0
	}, 5*time.Second, time.Millisecond)
	require.NoError(t, model.DB.First(&token, customers[0].tokenID).Error)
	require.Equal(t, 999000, token.RemainQuota)
	require.Equal(t, 1000, token.UsedQuota)
	before := calls.Load()
	require.GreaterOrEqual(t, invoke(1).Code, 400)
	require.Equal(t, before, calls.Load())
	require.EqualValues(t, 0, api("GET", "/api/user/self", customers[1].access, nil)["data"].(map[string]any)["quota"])
	logs := api("GET", fmt.Sprintf("/api/log/self?type=%d", model.LogTypeConsume), customers[0].access, nil)
	require.Equal(t, true, logs["success"])
	items := logs["data"].(map[string]any)["items"].([]any)
	require.Len(t, items, 1)
	entry := items[0].(map[string]any)
	require.EqualValues(t, 1000, entry["quota"])
	require.EqualValues(t, 11, entry["prompt_tokens"])
	require.EqualValues(t, 3, entry["completion_tokens"])
	other := api("GET", fmt.Sprintf("/api/log/self?type=%d&user_id=%d", model.LogTypeConsume, after.Id), customers[1].access, nil)
	require.Equal(t, true, other["success"])
	require.Empty(t, other["data"].(map[string]any)["items"])
	denied := api("POST", fmt.Sprintf("/api/token/%d/key", customers[0].tokenID), customers[1].access, nil)
	require.Equal(t, false, denied["success"])
	// Close the fixture's registration window and prove rejection before return.
	common.RegisterEnabled, common.PasswordRegisterEnabled = false, false
	require.Equal(t, false, api("POST", "/api/user/register", "", map[string]any{"username": "c1-closed", "password": "Synthetic-C1-Only!2026"})["success"])
	t.Log("success charge=1000; failure refunded; zero-wallet blocked before upstream; cross-user wallet/log/key isolation passed; registration closed")
}
