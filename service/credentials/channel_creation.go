// Copyright (c) QuantumNous and contributors.
package credentials

import (
	"context"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ValidateOpenAIKeyBundle bounds an opaque newline-separated bundle. Slot count
// is stable on rotation so existing disabled slots retain their meaning.
func ValidateOpenAIKeyBundle(ch model.Channel, secret string) error {
	if secret == "" || len(secret) > 65536 {
		return credentialcrypto.ErrUnavailable
	}
	keys := strings.Split(secret, "\n")
	if len(keys) > 128 || (!ch.ChannelInfo.IsMultiKey && len(keys) != 1) || (ch.ChannelInfo.IsMultiKey && len(keys) != ch.ChannelInfo.MultiKeySize) {
		return credentialcrypto.ErrUnavailable
	}
	for _, key := range keys {
		if key == "" || len(key) > 512 || strings.ContainsAny(key, "\r\t ") {
			return credentialcrypto.ErrUnavailable
		}
	}
	return nil
}

// OpenAIManagedConfiguration deliberately excludes alternate secret writers and
// task/proxy/header overrides until they have their own credential review.
func OpenAIManagedConfiguration(ch model.Channel) bool {
	if ch.Type != constant.ChannelTypeOpenAI || ch.Other != "" || common.TLSInsecureSkipVerify || common.DebugEnabled {
		return false
	}
	// Accept the current form's inert defaults, not arbitrary JSON configuration.
	for _, raw := range []string{ch.OtherSettings, valueOrEmpty(ch.Setting)} {
		if raw == "" {
			continue
		}
		var fields map[string]any
		if common.UnmarshalJsonStr(raw, &fields) != nil || fields == nil {
			return false
		}
		for key, value := range fields {
			switch key {
			case "force_format", "thinking_to_content", "pass_through_body_enabled", "responses_websocket_enabled", "system_prompt_override", "allow_service_tier", "disable_store", "allow_safety_identifier", "allow_include_obfuscation", "allow_inference_geo", "disable_task_polling_sleep", "upstream_model_update_check_enabled", "upstream_model_update_auto_sync_enabled":
				if value != false {
					return false
				}
			case "proxy", "system_prompt":
				if value != "" {
					return false
				}
			case "upstream_model_update_ignored_models", "upstream_model_update_last_detected_models":
				items, ok := value.([]any)
				if !ok || len(items) != 0 {
					return false
				}
			case "upstream_model_update_last_check_time":
				if value != float64(0) {
					return false
				}
			default:
				return false
			}
		}
	}
	return (ch.HeaderOverride == nil || *ch.HeaderOverride == "") &&
		(ch.ParamOverride == nil || *ch.ParamOverride == "") && !common.TLSInsecureSkipVerify && !common.DebugEnabled
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// CreateOpenAIChannels commits encrypted bundles, channel rows and abilities
// together. No plaintext is submitted to a channel SQL statement or SQL logger.
func (s *Store) CreateOpenAIChannels(ctx context.Context, actor Principal, channels []model.Channel) error {
	if len(channels) == 0 || len(channels) > 128 {
		return credentialcrypto.ErrUnavailable
	}
	for _, ch := range channels {
		if !OpenAIManagedConfiguration(ch) || ch.CredentialRef != "" || ValidateOpenAIKeyBundle(ch, ch.Key) != nil {
			return credentialcrypto.ErrUnavailable
		}
	}
	err := s.DB.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		local := *s
		local.DB = tx
		for _, ch := range channels {
			secret := []byte(ch.Key)
			ch.Key, ch.Keys = "", nil
			head, err := local.Create(ctx, actor, true, "openai", "channel_key_bundle", "relay", secret)
			clear(secret)
			if err != nil {
				return err
			}
			ch.Id, ch.CredentialRef, ch.Status = 0, head.ID, common.ChannelStatusManuallyDisabled
			ch.UpstreamBudgetEnabled = false
			ch.ChannelInfo = model.ChannelInfo{IsMultiKey: ch.ChannelInfo.IsMultiKey, MultiKeySize: ch.ChannelInfo.MultiKeySize, MultiKeyMode: ch.ChannelInfo.MultiKeyMode}
			if err := tx.Create(&ch).Error; err != nil {
				return err
			}
			if err := ch.AddAbilities(tx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return credentialcrypto.ErrUnavailable
	}
	return nil
}
