// Copyright (c) QuantumNous and contributors.
package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIPilotAuthenticatedSettlement(t *testing.T) {
	require.NoError(t, i18n.Init())
	for _, scenario := range []string{"ordinary", "stream", "retry", "failure", "timeout", "interrupted", "insufficient", "invalid_token"} {
		t.Run(scenario, func(t *testing.T) {
			oldDB, oldLog := model.DB, model.LOG_DB
			t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLog })
			setupRelayRouterTestDB(t)
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
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"pilot-e2e":0.002}`))
			t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(prices)) })
			initial := 1000000
			if scenario == "insufficient" {
				initial = 0
			}
			user := model.User{Username: "pilot-e2e", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", Quota: initial}
			require.NoError(t, model.DB.Create(&user).Error)
			token := model.Token{UserId: user.Id, Key: "syntheticpilotclient", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000000}
			require.NoError(t, model.DB.Create(&token).Error)
			var calls atomic.Int32
			release := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				n := calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer syntheticpilotsupplier" {
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
					_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"synthetic failure"}}`)
					return
				}
				if scenario == "stream" || scenario == "interrupted" {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"id\":\"mock\",\"model\":\"pilot-e2e\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
					if scenario == "interrupted" {
						w.(http.Flusher).Flush()
						select {
						case <-r.Context().Done():
						case <-release:
						}
						return
					}
					_, _ = io.WriteString(w, "data: {\"id\":\"mock\",\"model\":\"pilot-e2e\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":3,\"total_tokens\":14}}\n\ndata: [DONE]\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"mock","model":"pilot-e2e","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`)
			}))
			defer upstream.Close()
			defer close(release)
			url := upstream.URL
			channel := model.Channel{Type: constant.ChannelTypeOpenAI, Key: "syntheticpilotsupplier", Name: "pilot", Status: common.ChannelStatusEnabled, BaseURL: &url, Models: "pilot-e2e", Group: "default"}
			require.NoError(t, model.DB.Create(&channel).Error)
			require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "pilot-e2e", ChannelId: channel.Id, Enabled: true}).Error)
			engine := gin.New()
			engine.Use(middleware.RequestId())
			SetRelayRouter(engine)
			stream := scenario == "stream" || scenario == "interrupted"
			body := `{"model":"pilot-e2e","messages":[{"role":"user","content":"hi"}],"max_tokens":16}`
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
			engine.ServeHTTP(w, r)
			if scenario == "failure" || scenario == "timeout" {
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
				require.NotContains(t, log.Other+log.Content, "syntheticpilotclient")
			}
			require.Equal(t, charged, initial-after.Quota, w.Body.String())
			require.Equal(t, charged, 1000000-afterToken.RemainQuota)
			require.LessOrEqual(t, len(logs), 1, "retries and stream termination must not settle twice")
			require.NotContains(t, w.Body.String(), "syntheticpilotsupplier")
			if scenario == "ordinary" || scenario == "stream" || scenario == "retry" {
				require.Equal(t, 200, w.Code, w.Body.String())
				require.Len(t, logs, 1)
				require.Positive(t, charged)
				require.Equal(t, 11, logs[0].PromptTokens)
				require.Equal(t, 3, logs[0].CompletionTokens)
				require.Equal(t, channel.Id, logs[0].ChannelId)
				require.Contains(t, w.Body.String(), "hello")
			} else if scenario != "interrupted" {
				require.GreaterOrEqual(t, w.Code, 400, w.Body.String())
				require.Zero(t, charged)
			}
			if scenario == "invalid_token" || scenario == "insufficient" {
				require.Zero(t, calls.Load())
			}
			if scenario == "retry" {
				require.Equal(t, int32(2), calls.Load())
			}
			if scenario == "interrupted" {
				require.Equal(t, int32(1), calls.Load(), "never replay a stream after output")
				require.Len(t, logs, 1)
				require.Contains(t, logs[0].Other, `"status":"error"`)
			}

		})
	}
}
