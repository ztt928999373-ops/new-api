package credentials

// FieldClassification is an inventory decision, never authorization to migrate
// or encrypt a value. A provider-specific parser and migration approval remain
// necessary, even when its storage field is known to contain credentials.
type FieldClassification string

const (
	SupplierCredential    FieldClassification = "supplier_credential"
	OAuthCredential       FieldClassification = "oauth_credential"
	ThirdPartySecret      FieldClassification = "third_party_secret"
	SecretContainer       FieldClassification = "secret_capable_container"
	PublicConfiguration   FieldClassification = "public_configuration"
	PluginIdentifier      FieldClassification = "plugin_identifier"
	PendingClassification FieldClassification = "pending_confirmation"
)

var secretFieldInventory = map[string]FieldClassification{
	"channel.key":                         SupplierCredential,
	"codex.access_token":                  OAuthCredential,
	"codex.refresh_token":                 OAuthCredential,
	"codex.id_token":                      OAuthCredential,
	"vertex.private_key":                  SupplierCredential,
	"channel.header_override":             SecretContainer,
	"channel.param_override":              SecretContainer,
	"channel.other":                       SecretContainer,
	"channel.setting":                     SecretContainer,
	"channel.settings":                    SecretContainer,
	"channel.other_settings":              SecretContainer,
	"channel.base_url":                    SecretContainer,
	"channel.proxy":                       SecretContainer,
	"task.private_data.key":               SupplierCredential,
	"task.private_data.plugin_state":      SecretContainer,
	"task.private_data.result_url":        SecretContainer,
	"task.data":                           SecretContainer,
	"system_task.payload":                 SecretContainer,
	"system_task.state":                   SecretContainer,
	"system_task.result":                  SecretContainer,
	"system_task.error":                   SecretContainer,
	"options.SMTPToken":                   ThirdPartySecret,
	"options.ResendApiKey":                ThirdPartySecret,
	"options.GitHubClientSecret":          ThirdPartySecret,
	"options.LinuxDOClientSecret":         ThirdPartySecret,
	"options.TurnstileSecretKey":          ThirdPartySecret,
	"options.StripeApiSecret":             ThirdPartySecret,
	"options.StripeWebhookSecret":         ThirdPartySecret,
	"options.CreemApiKey":                 ThirdPartySecret,
	"options.CreemWebhookSecret":          ThirdPartySecret,
	"custom_oauth_provider.client_secret": ThirdPartySecret,
	"task_artifact_store.s3_secret_key":   ThirdPartySecret,
	"channel.name":                        PublicConfiguration,
	"channel.model_mapping":               PublicConfiguration,
	"channel.models":                      PublicConfiguration,
	"channel.azure_api_version":           PublicConfiguration,
	"channel.vertex_project_id":           PublicConfiguration,
	"channel.task_plugin_key":             PluginIdentifier,
	"channel.task_extend_plugin_keys":     PluginIdentifier,
	"task.plugin_snapshot.key":            PluginIdentifier,
}

// ClassifySecretField uses exact, case-sensitive qualified paths. Unknown
// fields, including custom JSON and merely Key-suffixed names, stay pending.
func ClassifySecretField(path string) FieldClassification {
	if class, ok := secretFieldInventory[path]; ok {
		return class
	}
	return PendingClassification
}

// SecretFieldInventory returns an independent inventory for audit tooling.
func SecretFieldInventory() map[string]FieldClassification {
	copy := make(map[string]FieldClassification, len(secretFieldInventory))
	for path, class := range secretFieldInventory {
		copy[path] = class
	}
	return copy
}

// CredentialFormatReview is independent of field classification. The only
// reviewed format still requires MigrateDisabledOpenAI's channel/configuration
// checks; it is not approval for automatic or production migration.
func CredentialFormatReview(provider, credentialType string) string {
	if provider == "openai" && credentialType == "channel_key_bundle" {
		return "restricted_openai_only"
	}
	return "pending_confirmation"
}
