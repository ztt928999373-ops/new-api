package kitutil

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
)

const Redacted = "***"

var (
	logPEM   = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
	logURL   = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	logAuth  = regexp.MustCompile(`(?i)\b(?:bearer|basic)[ ;]+[a-z0-9_./+=:-]+`)
	logToken = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]+|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b`)
	// Explicit authentication labels, not a "contains Key" heuristic. This is
	// defense in depth for text logs, never a credential migration classifier.
	logSecretField = regexp.MustCompile(`(?i)(["']?\b(?:api[_-]?key|api[_-]?secret|access[_-]?token|refresh[_-]?token|id[_-]?token|client[_-]?secret|secret[_-]?key|private[_-]?key|access[_-]?key[_-]?(?:id|secret)|secret|token|password|passwd|authorization|proxy-authorization|x-api-key|x-goog-api-key|x-auth-token|x-amz-credential|x-amz-security-token|x-amz-signature|signature|cookie|set-cookie)["']?\s*[:=]\s*)("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|\[[^\]\r\n]*\]|[^\s,;&}\]"']+)`)
)

// SanitizeLogText is the final safety net for diagnostic text. Opaque bodies,
// arbitrary headers and unknown JSON must be omitted at their source: no text
// matcher can recognize an unlabelled secret. No secret registry is retained.
// knownSecrets are request-scoped values, including custom authentication heads.
func SanitizeLogText(text string, knownSecrets ...string) string {
	// A known Channel.Key can itself be an OAuth/JSON or multi-Key bundle.
	// Treat all string members as secret here; this never classifies DB fields.
	values := append([]string(nil), knownSecrets...)
	for _, secret := range knownSecrets {
		trimmed := strings.TrimSpace(secret)
		if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
			continue
		}
		var value any
		if Unmarshal([]byte(trimmed), &value) != nil {
			continue
		}
		pending := []any{value}
		for len(pending) > 0 {
			last := len(pending) - 1
			value, pending = pending[last], pending[:last]
			switch v := value.(type) {
			case string:
				values = append(values, v)
			case []any:
				pending = append(pending, v...)
			case map[string]any:
				for _, member := range v {
					pending = append(pending, member)
				}
			}
		}
	}
	// Legacy newline multi-Key and pipe-delimited credentials can be echoed
	// one member at a time. This conservative log-only expansion neither
	// validates their format nor authorizes their migration.
	for _, value := range slices.Clone(values) {
		if strings.ContainsAny(value, "|\r\n") {
			values = append(values, strings.FieldsFunc(value, func(r rune) bool { return r == '|' || r == '\r' || r == '\n' })...)
		}
	}
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	for _, secret := range values {
		if secret == "" {
			continue
		}
		for _, value := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			text = strings.ReplaceAll(text, value, Redacted)
		}
		if encoded, err := Marshal(secret); err == nil && len(encoded) > 2 {
			text = strings.ReplaceAll(text, string(encoded[1:len(encoded)-1]), Redacted)
		}
	}
	text = logPEM.ReplaceAllString(text, Redacted)
	text = logURL.ReplaceAllStringFunc(text, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return "[URL omitted]"
		}
		// Paths, fragments and even unknown query names can contain credentials.
		return u.Scheme + "://" + u.Host + "/[URL-omitted]"
	})
	text = sanitizeCredentialText(text)
	// Prevent provider-controlled messages from forging additional log entries.
	return strings.NewReplacer("\r", `\r`, "\n", `\n`, "\x00", "").Replace(text)
}

func sanitizeCredentialText(text string) string {
	text = logPEM.ReplaceAllString(text, Redacted)
	text = logAuth.ReplaceAllString(text, "authentication "+Redacted)
	text = logSecretField.ReplaceAllString(text, "${1}"+Redacted)
	return logToken.ReplaceAllString(text, Redacted)
}
