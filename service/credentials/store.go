// Copyright (c) QuantumNous and contributors.
package credentials

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"gorm.io/gorm"
)

type Head struct {
	ID             string `gorm:"primaryKey;size:64" json:"id"`
	TenantID       string `gorm:"size:64;not null;index" json:"-"`
	OwnerType      string `gorm:"size:16;not null" json:"owner_type"`
	OwnerID        string `gorm:"size:64;not null;index" json:"-"`
	Environment    string `gorm:"size:64;not null" json:"-"`
	Provider       string `gorm:"size:64;not null" json:"provider"`
	CredentialType string `gorm:"size:64;not null" json:"credential_type"`
	Purpose        string `gorm:"size:64;not null" json:"purpose"`
	CurrentVersion int64  `gorm:"not null" json:"version"`
	Revision       int64  `gorm:"not null" json:"revision"`
	Status         string `gorm:"size:16;not null" json:"status"`
}

func (Head) TableName() string { return "supplier_credentials" }

type StoredVersion struct {
	CredentialID string `gorm:"primaryKey;size:64" json:"-"`
	Version      int64  `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Envelope     string `gorm:"type:text;not null" json:"-"`
}

func (StoredVersion) TableName() string { return "supplier_credential_versions" }

type Store struct {
	DB          *gorm.DB
	Environment string
	ProviderID  string
	Provider    credentialcrypto.KeyProvider
}

func (s *Store) CurrentVersion(ctx context.Context, id string) (uint64, error) {
	var h Head
	if s.DB.WithContext(ctx).First(&h, "id = ?", id).Error != nil || h.Status != "active" || h.Environment != s.Environment {
		return 0, credentialcrypto.ErrUnavailable
	}
	return uint64(h.CurrentVersion), nil
}

func (s *Store) Metadata(ctx context.Context, actor Principal, id string) (Head, error) {
	var h Head
	if s.DB.WithContext(ctx).First(&h, "id = ?", id).Error != nil || !owns(actor, h) || h.Environment != s.Environment {
		return Head{}, credentialcrypto.ErrUnavailable
	}
	return h, nil
}

func (s *Store) Versions(ctx context.Context, actor Principal, id string) ([]int64, error) {
	if _, err := s.Metadata(ctx, actor, id); err != nil {
		return nil, err
	}
	var versions []int64
	if s.DB.WithContext(ctx).Model(&StoredVersion{}).Where("credential_id = ?", id).Order("version").Pluck("version", &versions).Error != nil {
		return nil, credentialcrypto.ErrUnavailable
	}
	return versions, nil
}

// MigrateSchema creates only metadata tables; it never migrates legacy secrets.
func (s *Store) MigrateSchema() error {
	return s.DB.AutoMigrate(&Head{}, &StoredVersion{}, &AccessEvent{})
}

type AccessEvent struct {
	ID           uint64 `gorm:"primaryKey"`
	CredentialID string `gorm:"size:64;index"`
	Version      uint64
	ActorID      string `gorm:"size:64"`
	Outcome      string `gorm:"size:32"`
	CreatedAt    int64  `gorm:"autoCreateTime"`
}

func (AccessEvent) TableName() string { return "supplier_credential_access_events" }
func (s *Store) Record(ctx context.Context, event Event) error {
	if s.DB.WithContext(ctx).Create(&AccessEvent{CredentialID: event.CredentialID, Version: event.Version, ActorID: event.ActorID, Outcome: event.Outcome}).Error != nil {
		return credentialcrypto.ErrUnavailable
	}
	return nil
}

func identity(h Head, version int64) credentialcrypto.Identity {
	return credentialcrypto.Identity{Environment: h.Environment, TenantID: h.TenantID, OwnerType: h.OwnerType, OwnerID: h.OwnerID, CredentialID: h.ID, Provider: h.Provider, CredentialType: h.CredentialType, Purpose: h.Purpose, Version: uint64(version)}
}
func owns(actor Principal, h Head) bool {
	return actor.UserID != "" && actor.TenantID == h.TenantID && ((h.OwnerType == "byok" && h.OwnerID == actor.UserID) || (h.OwnerType == "platform" && actor.PlatformOperator))
}

// Create derives ownership from authenticated server state, never from input owner_id.
func (s *Store) Create(ctx context.Context, actor Principal, platform bool, provider, kind, purpose string, secret []byte) (Head, error) {
	if actor.UserID == "" || actor.TenantID == "" || (platform && !actor.PlatformOperator) {
		return Head{}, credentialcrypto.ErrUnavailable
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return Head{}, credentialcrypto.ErrUnavailable
	}
	h := Head{ID: hex.EncodeToString(b), TenantID: actor.TenantID, OwnerType: "byok", OwnerID: actor.UserID, Environment: s.Environment, Provider: provider, CredentialType: kind, Purpose: purpose, CurrentVersion: 1, Revision: 1, Status: "active"}
	if platform {
		h.OwnerType, h.OwnerID = "platform", "platform"
	}
	e, err := credentialcrypto.Seal(ctx, s.Provider, s.ProviderID, identity(h, 1), secret)
	if err != nil {
		return Head{}, credentialcrypto.ErrUnavailable
	}
	data, err := common.Marshal(e)
	if err != nil {
		return Head{}, credentialcrypto.ErrUnavailable
	}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&h).Error; err != nil {
			return err
		}
		if err := tx.Create(&StoredVersion{CredentialID: h.ID, Version: 1, Envelope: string(data)}).Error; err != nil {
			return err
		}
		return tx.Create(&AccessEvent{CredentialID: h.ID, Version: 1, ActorID: actor.UserID, Outcome: "created"}).Error
	})
	if err != nil {
		return Head{}, credentialcrypto.ErrUnavailable
	}
	return h, nil
}

func (s *Store) GetForUse(ctx context.Context, id string, version uint64) (Record, error) {
	if version == 0 || version > 1<<63-1 {
		return Record{}, credentialcrypto.ErrUnavailable
	}
	var h Head
	var v StoredVersion
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&h, "id = ?", id).Error; err != nil {
			return err
		}
		if h.Status != "active" || h.Environment != s.Environment {
			return credentialcrypto.ErrUnavailable
		}
		return tx.First(&v, "credential_id = ? AND version = ?", id, int64(version)).Error
	})
	if err != nil {
		return Record{}, credentialcrypto.ErrUnavailable
	}
	var e credentialcrypto.Envelope
	if common.UnmarshalJsonStr(v.Envelope, &e) != nil {
		return Record{}, credentialcrypto.ErrUnavailable
	}
	return Record{Identity: identity(h, int64(version)), Envelope: e, Status: h.Status}, nil
}

// Replace uses an optimistic revision fence; competing writes cannot silently win.
func (s *Store) Replace(ctx context.Context, actor Principal, id string, revision int64, secret []byte) (Head, error) {
	var h Head
	if s.DB.WithContext(ctx).First(&h, "id = ?", id).Error != nil || !owns(actor, h) || h.Environment != s.Environment || h.Status == "revoked" || h.CurrentVersion >= 1<<63-1 {
		return Head{}, credentialcrypto.ErrUnavailable
	}
	if h.CredentialType == "channel_key_bundle" {
		var channels []model.Channel
		if s.DB.WithContext(ctx).Where("credential_ref = ?", id).Find(&channels).Error != nil {
			return Head{}, credentialcrypto.ErrUnavailable
		}
		for _, channel := range channels {
			if channel.ChannelInfo.IsMultiKey {
				candidate := model.Channel{Key: string(secret)}
				if len(candidate.GetKeys()) != channel.ChannelInfo.MultiKeySize {
					return Head{}, credentialcrypto.ErrUnavailable
				}
			}
		}
	}
	next := h.CurrentVersion + 1
	e, err := credentialcrypto.Seal(ctx, s.Provider, s.ProviderID, identity(h, next), secret)
	if err != nil {
		return Head{}, credentialcrypto.ErrUnavailable
	}
	data, err := common.Marshal(e)
	if err != nil {
		return Head{}, credentialcrypto.ErrUnavailable
	}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&Head{}).Where("id = ? AND revision = ? AND status <> ?", id, revision, "revoked").Updates(map[string]any{"current_version": next, "revision": revision + 1})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return credentialcrypto.ErrUnavailable
		}
		if err := tx.Create(&StoredVersion{CredentialID: id, Version: next, Envelope: string(data)}).Error; err != nil {
			return err
		}
		return tx.Create(&AccessEvent{CredentialID: id, Version: uint64(next), ActorID: actor.UserID, Outcome: "replaced"}).Error
	})
	if err != nil {
		return Head{}, credentialcrypto.ErrUnavailable
	}
	h.CurrentVersion, h.Revision = next, revision+1
	return h, nil
}

func (s *Store) SetStatus(ctx context.Context, actor Principal, id string, revision int64, status string) error {
	if status != "active" && status != "disabled" && status != "revoked" {
		return credentialcrypto.ErrUnavailable
	}
	var h Head
	if s.DB.WithContext(ctx).First(&h, "id = ?", id).Error != nil || !owns(actor, h) || h.Environment != s.Environment || h.Status == "revoked" {
		return credentialcrypto.ErrUnavailable
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&Head{}).Where("id = ? AND revision = ? AND status <> ?", id, revision, "revoked").Updates(map[string]any{"status": status, "revision": revision + 1})
		if result.Error != nil || result.RowsAffected != 1 {
			return credentialcrypto.ErrUnavailable
		}
		return tx.Create(&AccessEvent{CredentialID: id, Version: uint64(h.CurrentVersion), ActorID: actor.UserID, Outcome: status}).Error
	})
	if err != nil {
		return credentialcrypto.ErrUnavailable
	}
	return nil
}

// RewrapVersion changes only DEK protection. It never replaces supplier secrets.
func (s *Store) RewrapVersion(ctx context.Context, actor Principal, id string, version, revision int64) error {
	h, err := s.Metadata(ctx, actor, id)
	if err != nil || h.Status == "revoked" || version <= 0 {
		return credentialcrypto.ErrUnavailable
	}
	var row StoredVersion
	if s.DB.WithContext(ctx).First(&row, "credential_id = ? AND version = ?", id, version).Error != nil {
		return credentialcrypto.ErrUnavailable
	}
	var envelope credentialcrypto.Envelope
	if common.UnmarshalJsonStr(row.Envelope, &envelope) != nil || envelope.ProviderID != s.ProviderID {
		return credentialcrypto.ErrUnavailable
	}
	updated, err := credentialcrypto.Rewrap(ctx, s.Provider, identity(h, version), envelope)
	if err != nil {
		return credentialcrypto.ErrUnavailable
	}
	data, err := common.Marshal(updated)
	if err != nil {
		return credentialcrypto.ErrUnavailable
	}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fence := tx.Model(&Head{}).Where("id = ? AND revision = ? AND status <> ?", id, revision, "revoked").Update("revision", revision+1)
		if fence.Error != nil || fence.RowsAffected != 1 {
			return credentialcrypto.ErrUnavailable
		}
		result := tx.Model(&StoredVersion{}).Where("credential_id = ? AND version = ?", id, version).Update("envelope", string(data))
		if result.Error != nil || result.RowsAffected != 1 {
			return credentialcrypto.ErrUnavailable
		}
		return tx.Create(&AccessEvent{CredentialID: id, Version: uint64(version), ActorID: actor.UserID, Outcome: "rewrapped"}).Error
	})
	if err != nil {
		return credentialcrypto.ErrUnavailable
	}
	return nil
}
