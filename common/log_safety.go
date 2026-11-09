package common

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/gin-gonic/gin"
)

// SanitizeLogText does not retain the supplied request-scoped secrets.
func SanitizeLogText(text string, secrets ...string) string {
	return kitutil.SanitizeLogText(text, secrets...)
}

// SanitizeRequestDiagnostic removes values even from custom headers whose names
// are not in the standard authentication vocabulary. Bodies are never read.
func SanitizeRequestDiagnostic(ctx context.Context, request *http.Request, text string) string {
	var secrets []string
	if c, ok := ctx.(*gin.Context); ok {
		secrets = append(secrets, GetContextKeyString(c, constant.ContextKeyChannelKey), GetContextKeyString(c, constant.ContextKeyTokenKey))
		if request == nil {
			request = c.Request
		}
	}
	if request != nil {
		for _, values := range request.Header {
			secrets = append(secrets, values...)
			for _, value := range values {
				if scheme, token, ok := strings.Cut(value, " "); ok && (strings.EqualFold(scheme, "Bearer") || strings.EqualFold(scheme, "Basic")) {
					secrets = append(secrets, token)
				}
			}
		}
		if request.URL != nil {
			for _, values := range request.URL.Query() {
				secrets = append(secrets, values...)
			}
		}
	}
	return SanitizeLogText(text, secrets...)
}

type outboundDiagnosticError struct {
	cause  error
	reason string
}

func (e outboundDiagnosticError) Error() string { return "upstream transport error: " + e.reason }
func (e outboundDiagnosticError) Unwrap() error { return e.cause }

// SafeOutboundError preserves errors.Is/As without publishing a transport's
// arbitrary error text, which can contain URLs, credentials or request headers.
func SafeOutboundError(err error) error {
	if err == nil {
		return nil
	}
	reason := "request_failed"
	var networkError net.Error
	switch {
	case errors.Is(err, context.Canceled):
		reason = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "timeout"
	case errors.As(err, &networkError) && networkError.Timeout():
		reason = "timeout"
	}
	return outboundDiagnosticError{cause: err, reason: reason}
}
