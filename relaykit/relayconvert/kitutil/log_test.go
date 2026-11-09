package kitutil

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDiagnosticRedaction(t *testing.T) {
	for _, input := range []string{
		`{"access_token":"SYNTHETIC_SENTINEL","refresh_token":"SYNTHETIC_SENTINEL"}`,
		"Authorization: Bearer SYNTHETIC_SENTINEL",
		"Proxy-Authorization: Basic SYNTHETIC_SENTINEL",
		"map[X-Api-Key:[SYNTHETIC_SENTINEL] Cookie:[SYNTHETIC_SENTINEL]]",
		`{"private_key":"-----BEGIN PRIVATE KEY-----\\nSYNTHETIC_SENTINEL\\n-----END PRIVATE KEY-----"}`,
		"https://user:SYNTHETIC_SENTINEL@provider.test/private/SYNTHETIC_SENTINEL?custom=SYNTHETIC_SENTINEL#SYNTHETIC_SENTINEL",
		"http://provider.test/%broken?opaque=SYNTHETIC_SENTINEL",
	} {
		t.Run(input, func(t *testing.T) {
			clean := SanitizeLogText(input)
			assert.NotContains(t, clean, "SYNTHETIC_SENTINEL")
			assert.Equal(t, clean, SanitizeLogText(clean), "nested logger hooks must be idempotent")
		})
	}
	assert.Equal(t, "provider=openai channel_id=42 credential_id=c1 status=401 reason=authentication_failed", SanitizeLogText("provider=openai channel_id=42 credential_id=c1 status=401 reason=authentication_failed"))
	assert.Equal(t, "opaque="+Redacted, SanitizeLogText("opaque=synthetic-custom-value", "synthetic-custom-value"))
	assert.Equal(t, "echo="+Redacted, SanitizeLogText("echo=synthetic-oauth-member", `{"access_token":"synthetic-oauth-member"}`))
	assert.Equal(t, "echo="+Redacted, SanitizeLogText("echo=synthetic-slot", `["synthetic-slot"]`))
	assert.Equal(t, "echo="+Redacted, SanitizeLogText("echo=synthetic-compound-secret", "synthetic-id|synthetic-compound-secret"))
	assert.Equal(t, "echo="+Redacted, SanitizeLogText("echo=synthetic-slot-two", "synthetic-slot-one\nsynthetic-slot-two"))
	assert.NotContains(t, SanitizeLogText("event=failed\nFORGED\rENTRY"), "\n")
	assert.NotContains(t, SanitizeLogText("event=failed\nFORGED\rENTRY"), "\r")
	// Credential-free plugin keys and ordinary model fields are not secrets.
	assert.Equal(t, "plugin_key=image model=gpt-4o", SanitizeLogText("plugin_key=image model=gpt-4o"))
	assert.False(t, strings.Contains(MaskSensitiveInfo(`refresh_token=synthetic-refresh`), "synthetic-refresh"))
	assert.NotContains(t, MaskSensitiveInfo(`Post "https://user:synthetic-pass@provider.test/%ZZ?opaque=synthetic-query"`), "synthetic-")
}

func TestErrorHooksRemainDistinct(t *testing.T) {
	previousError := logError.Load()
	previousSystemError := logSystemError.Load()
	t.Cleanup(func() {
		logError.Store(previousError)
		logSystemError.Store(previousSystemError)
	})

	var ordinaryMessages []string
	var systemMessages []string
	SetLogging(nil, func(message string) {
		ordinaryMessages = append(ordinaryMessages, message)
	})
	SetSystemErrorLogging(func(message string) {
		systemMessages = append(systemMessages, message)
	})

	LogError("invalid dto")
	LogSystemError("converter failure")

	assert.Equal(t, []string{"invalid dto"}, ordinaryMessages)
	assert.Equal(t, []string{"converter failure"}, systemMessages)
}
