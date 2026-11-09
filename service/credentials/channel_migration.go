// Copyright (c) QuantumNous and contributors.
package credentials

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type BackupReceipt struct {
	Path   string
	SHA256 string
}

// VerifyBackup verifies an existing nonempty backup artifact, never makes or
// deletes one. Operators must additionally perform a restore/schema check.
func (b BackupReceipt) VerifyBackup() error {
	f, err := os.Open(b.Path)
	if err != nil {
		return credentialcrypto.ErrUnavailable
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil || n == 0 || hex.EncodeToString(h.Sum(nil)) != b.SHA256 {
		return credentialcrypto.ErrUnavailable
	}
	return nil
}

// MigrateDisabledOpenAI is intentionally narrow. Unknown secret-bearing fields,
// OAuth, other suppliers and active channels are refused until their writers and
// task snapshots are migrated. No startup code invokes this operation.
func (s *Store) MigrateDisabledOpenAI(ctx context.Context, channelID int, backup BackupReceipt) (string, error) {
	if backup.VerifyBackup() != nil {
		return "", credentialcrypto.ErrUnavailable
	}
	var ref string
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ch model.Channel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&ch, channelID).Error; err != nil {
			return err
		}
		if ch.CredentialRef != "" {
			r, err := (&Store{DB: tx, Environment: s.Environment}).CurrentVersion(ctx, ch.CredentialRef)
			if err != nil || r == 0 || ch.Key != "" {
				return credentialcrypto.ErrUnavailable
			}
			ref = ch.CredentialRef
			return nil
		}
		if ch.Type != constant.ChannelTypeOpenAI || ch.Status != common.ChannelStatusManuallyDisabled || ch.Key == "" || ch.Other != "" || ch.OtherSettings != "" || (ch.Setting != nil && *ch.Setting != "") || (ch.HeaderOverride != nil && *ch.HeaderOverride != "") || (ch.ParamOverride != nil && *ch.ParamOverride != "") {
			return credentialcrypto.ErrUnavailable
		}
		local := *s
		local.DB = tx
		h, err := local.Create(ctx, Principal{TenantID: "default", UserID: "migration", PlatformOperator: true}, true, "openai", "channel_key_bundle", "relay", []byte(ch.Key))
		if err != nil {
			return err
		}
		r, err := local.GetForUse(ctx, h.ID, 1)
		if err != nil {
			return err
		}
		plain, err := credentialcrypto.Open(ctx, s.Provider, s.ProviderID, r.Identity, r.Envelope)
		if err != nil {
			return err
		}
		defer clear(plain)
		if string(plain) != ch.Key {
			return credentialcrypto.ErrUnavailable
		}
		result := tx.Model(&model.Channel{}).Where("id = ? AND (credential_ref = ? OR credential_ref IS NULL)", channelID, "").Updates(map[string]any{"key": "", "credential_ref": h.ID})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return credentialcrypto.ErrUnavailable
		}
		ref = h.ID
		return nil
	})
	if err != nil {
		return "", credentialcrypto.ErrUnavailable
	}
	return ref, nil
}
