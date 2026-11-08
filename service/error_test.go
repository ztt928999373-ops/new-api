package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type diagnosticRoundTripper func(*http.Request) (*http.Response, error)

func (f diagnosticRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestOAuthDiagnosticErrorsExcludeTokens(t *testing.T) {
	for _, response := range []bool{false, true} {
		client := &http.Client{Transport: diagnosticRoundTripper(func(req *http.Request) (*http.Response, error) {
			require.NoError(t, req.ParseForm())
			require.Equal(t, "synthetic-refresh-sentinel", req.Form.Get("refresh_token"))
			if !response {
				return nil, fmt.Errorf("opaque=synthetic-refresh-sentinel Header: synthetic-access-sentinel: %w", context.DeadlineExceeded)
			}
			return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"access_token":"synthetic-access-sentinel","refresh_token":"synthetic-refresh-sentinel","error_description":"synthetic-access-sentinel"}`)), Header: http.Header{}, Request: req}, nil
		})}
		_, err := refreshCodexOAuthToken(context.Background(), client, "https://mock.invalid/token", "synthetic-client", "synthetic-refresh-sentinel")
		require.Error(t, err)
		require.NotContains(t, err.Error(), "synthetic-")
		if !response {
			require.True(t, errors.Is(err, context.DeadlineExceeded))
			require.Contains(t, err.Error(), "timeout")
		} else {
			require.Contains(t, err.Error(), "status=401")
		}
	}
}

func TestHTTPErrorResponsesExcludeCredentials(t *testing.T) {
	for _, debug := range []bool{false, true} {
		for _, showBody := range []bool{false, true} {
			for _, body := range []string{
				"arbitrary unlabelled synthetic-header-sentinel",
				`{"error":{"message":"echo synthetic-header-sentinel access_token=synthetic-access-sentinel","type":"authentication_error","param":"synthetic-header-sentinel","code":"invalid_api_key","metadata":{"headers":{"X-Custom":"synthetic-header-sentinel"}}}}`,
			} {
				withDebugEnabled(t, debug)
				req, err := http.NewRequest(http.MethodPost, "https://provider.invalid", nil)
				require.NoError(t, err)
				req.Header.Set("X-Custom", "synthetic-header-sentinel")
				resp := &http.Response{StatusCode: 401, Request: req, Body: io.NopCloser(strings.NewReader(body))}
				apiErr := RelayErrorHandler(context.Background(), resp, showBody)
				encoded, err := common.Marshal(apiErr.ToOpenAIError())
				require.NoError(t, err)
				require.NotContains(t, string(encoded), "synthetic-")
				require.NotContains(t, apiErr.Error(), "synthetic-")
				require.Equal(t, 401, apiErr.StatusCode)
			}
		}
	}
}

func TestProviderErrorMetadataCannotBypassHTTPHandler(t *testing.T) {
	local := types.NewOpenAIError(errors.New("model is required"), types.ErrorCodeInvalidRequest, 400)
	require.Equal(t, types.ErrorCodeInvalidRequest, local.ToOpenAIError().Code)
	for _, code := range []any{"invalid_api_key", map[string]any{"X-Custom": "synthetic-opaque-header"}} {
		apiErr := types.WithOpenAIError(types.OpenAIError{
			Message: "authentication failed", Type: "authentication_error", Code: code,
			Param:    "refresh_token=synthetic-refresh-sentinel",
			Metadata: common.RawMessage(`{"headers":{"X-Custom":"synthetic-opaque-header"}}`),
		}, 401)
		require.NotContains(t, apiErr.Error(), "synthetic-")
		require.Empty(t, apiErr.Metadata)
		for _, output := range []any{apiErr.ToOpenAIError(), apiErr.ToClaudeError()} {
			encoded, err := common.Marshal(output)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "synthetic-")
			require.Contains(t, string(encoded), "authentication")
		}
	}
}

func TestResetStatusCode(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		statusCode       int
		statusCodeConfig string
		expectedCode     int
	}{
		{
			name:             "map string value",
			statusCode:       429,
			statusCodeConfig: `{"429":"503"}`,
			expectedCode:     503,
		},
		{
			name:             "map int value",
			statusCode:       429,
			statusCodeConfig: `{"429":503}`,
			expectedCode:     503,
		},
		{
			name:             "skip invalid string value",
			statusCode:       429,
			statusCodeConfig: `{"429":"bad-code"}`,
			expectedCode:     429,
		},
		{
			name:             "skip status code 200",
			statusCode:       200,
			statusCodeConfig: `{"200":503}`,
			expectedCode:     200,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			newAPIError := &types.NewAPIError{
				StatusCode: tc.statusCode,
			}
			ResetStatusCode(newAPIError, tc.statusCodeConfig)
			require.Equal(t, tc.expectedCode, newAPIError.StatusCode)
		})
	}
}

func TestRelayErrorHandlerOmitsInvalidJSONBodyInLog(t *testing.T) {
	withDebugEnabled(t, false)

	body := strings.Repeat("b", common.LocalLogContentLimit+256)
	var logBuffer bytes.Buffer

	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logBuffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = oldWriter
		common.LogWriterMu.Unlock()
	})

	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, "bad response status code 500", newAPIError.Error())
	require.Contains(t, logBuffer.String(), "[body omitted;")
	require.Contains(t, logBuffer.String(), fmt.Sprintf("original_length=%d", len(body)))
	require.NotContains(t, logBuffer.String(), strings.Repeat("b", common.LocalLogContentLimit+1))
}

func TestRelayErrorHandlerKeepsStructuredErrorMessage(t *testing.T) {
	message := strings.Repeat("c", common.LocalLogContentLimit+256)
	body := `{"message":"` + message + `"}`
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, message, newAPIError.Error())
}

func TestRelayErrorHandlerKeepsOpenAIErrorMessage(t *testing.T) {
	message := strings.Repeat("d", common.LocalLogContentLimit+256)
	body := `{"error":{"message":"` + message + `","type":"server_error","code":"server_error"}}`
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, message, newAPIError.Error())
}

func TestRelayErrorHandlerOmitsInvalidJSONBodyInDebugLog(t *testing.T) {
	withDebugEnabled(t, true)

	body := strings.Repeat("e", common.LocalLogContentLimit+256)
	var logBuffer bytes.Buffer

	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logBuffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = oldWriter
		common.LogWriterMu.Unlock()
	})

	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.NotContains(t, logBuffer.String(), "[truncated")
	require.Contains(t, logBuffer.String(), fmt.Sprintf("original_length=%d", len(body)))
	require.NotContains(t, logBuffer.String(), body)
}

func withDebugEnabled(t *testing.T, enabled bool) {
	t.Helper()

	oldDebug := common.DebugEnabled
	common.DebugEnabled = enabled
	t.Cleanup(func() {
		common.DebugEnabled = oldDebug
	})
}
