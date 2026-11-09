// Copyright (c) QuantumNous and contributors.
package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Synthetic fixtures exercise the production transport and response parsers.
// They are not evidence of a working supplier account or of billing settlement.
func TestOpenAIPilotMockTransportAndUsage(t *testing.T) {
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	service.InitHttpClient()
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "stream"}[stream], func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer synthetic-pilot-key", r.Header.Get("Authorization"))
				require.Equal(t, "/v1/chat/completions", r.URL.Path)
				var request dto.GeneralOpenAIRequest
				require.NoError(t, common.DecodeJson(r.Body, &request))
				require.Equal(t, "pilot-model", request.Model)
				require.NotNil(t, request.Stream)
				require.Equal(t, stream, *request.Stream)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"id\":\"mock\",\"model\":\"pilot-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"mock\",\"model\":\"pilot-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":3,\"total_tokens\":14}}\n\ndata: [DONE]\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"mock","object":"chat.completion","model":"pilot-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`)
			}))
			defer upstream.Close()
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set("Authorization", "Bearer client-token-must-not-forward")
			info := &relaycommon.RelayInfo{OriginModelName: "pilot-model", IsStream: stream, DisablePing: true, StartTime: time.Now(), RequestURLPath: "/v1/chat/completions", RelayFormat: types.RelayFormatOpenAI,
				ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI, ChannelBaseUrl: upstream.URL, ApiKey: "synthetic-pilot-key", UpstreamModelName: "pilot-model", SupportStreamOptions: true}}
			a := &Adaptor{}
			a.Init(info)
			request := &dto.GeneralOpenAIRequest{Model: "pilot-model", Stream: &stream, Messages: []dto.Message{{Role: "user", Content: "hi"}}}
			if stream {
				request.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
			}
			converted, err := a.ConvertOpenAIRequest(c, info, request)
			require.NoError(t, err)
			body, err := common.Marshal(converted)
			require.NoError(t, err)
			response, err := a.DoRequest(c, info, strings.NewReader(string(body)))
			require.NoError(t, err)
			usage, apiErr := a.DoResponse(c, response.(*http.Response), info)
			require.Nil(t, apiErr)
			got := usage.(*dto.Usage)
			require.Equal(t, 11, got.PromptTokens)
			require.Equal(t, 3, got.CompletionTokens)
			require.Contains(t, w.Body.String(), "hello")
			require.NotContains(t, w.Body.String(), "synthetic-pilot-key")
			if stream {
				require.Contains(t, w.Body.String(), "[DONE]")
			}
		})
	}
}

func TestOpenAIPilotMockErrors(t *testing.T) {
	for _, status := range []int{401, 429, 500} {
		resp := &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"synthetic rejection","type":"invalid_request_error","code":"pilot_rejected"}}`))}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		err := service.RelayErrorHandler(c, resp, false)
		require.NotNil(t, err)
		require.Equal(t, status, err.StatusCode)
	}
}
