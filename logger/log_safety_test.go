package logger_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	"github.com/QuantumNous/new-api/relay/channel/zhipu"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProviderDiagnosticSafety(t *testing.T) {
	var output bytes.Buffer
	common.LogWriterMu.Lock()
	oldWriter, oldErrorWriter := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = &output, &output
	common.LogWriterMu.Unlock()
	oldDebug := common.DebugEnabled
	common.DebugEnabled = true
	t.Cleanup(func() {
		common.DebugEnabled = oldDebug
		common.LogWriterMu.Lock()
		gin.DefaultWriter, gin.DefaultErrorWriter = oldWriter, oldErrorWriter
		common.LogWriterMu.Unlock()
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	c.Request.Header.Set("X-Custom-Auth", "synthetic-private-header")
	common.SetContextKey(c, constant.ContextKeyChannelKey, "synthetic-unlabelled-key")
	c.Set(common.RequestIdKey, "synthetic-unlabelled-key")
	logger.LogDebug(c, "provider=openai channel_id=42 credential_id=c1 echoed=synthetic-private-header value=synthetic-unlabelled-key")
	c.Set(common.RequestIdKey, "request-42")
	logger.LogInfo(c, "provider=openai event=authentication_failed")
	common.SysError(`refresh_token=synthetic-oauth-token`)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "synthetic-invalid-zhipu-key"}}
	headers := http.Header{}
	require.NoError(t, (&zhipu.Adaptor{}).SetupRequestHeader(c, &headers, info))
	require.Contains(t, output.String(), "provider=zhipu")
	require.Contains(t, output.String(), "invalid_credential_format")
	for _, secret := range []string{"synthetic-private-header", "synthetic-unlabelled-key", "synthetic-oauth-token", "synthetic-invalid-zhipu-key"} {
		require.NotContains(t, output.String(), secret)
	}
	require.Contains(t, output.String(), "channel_id=42 credential_id=c1")
	require.Contains(t, output.String(), "request-42")

	service.InitHttpClient()
	for _, channelType := range []int{constant.ChannelTypeOpenAI, constant.ChannelTypeOpenRouter} {
		info.ChannelType, info.ChannelId, info.ApiKey = channelType, 42, "synthetic-provider-key"
		headers = http.Header{}
		require.NoError(t, (&openai.Adaptor{}).SetupRequestHeader(c, &headers, info))
		u, err := url.Parse("https://provider.invalid/secret-path?opaque=synthetic-query-value")
		require.NoError(t, err)
		// net/http rejects the method before any outbound connection.
		req := &http.Request{Method: "invalid\nmethod", URL: u, Header: headers, Body: io.NopCloser(strings.NewReader("{}"))}
		_, err = channel.DoRequest(c, req, info)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "synthetic-")
		require.NotContains(t, output.String(), "synthetic-provider-key")
		require.NotContains(t, output.String(), "synthetic-query-value")
		require.Contains(t, output.String(), "event=upstream_request_failed")
	}
	wrapped := common.SafeOutboundError(context.DeadlineExceeded)
	require.True(t, errors.Is(wrapped, context.DeadlineExceeded))
	require.Contains(t, wrapped.Error(), "timeout")
}
