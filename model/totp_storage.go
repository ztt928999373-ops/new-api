// Copyright (c) QuantumNous and contributors.
package model

import (
	"context"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var ErrTOTPStorage = errors.New("TOTP storage unavailable")

const totpEnvelopePrefix = "totp:v1:"

// TOTPSecretStore is independent of supplier credentials and session secrets.
// Configure it only during exclusive startup/operations, never from HTTP input.
type TOTPSecretStore struct {
	Environment string
	Namespace   string
	Provider    credentialcrypto.KeyProvider
}

var totpStore atomic.Pointer[TOTPSecretStore]

func SetTOTPSecretStore(s *TOTPSecretStore) *TOTPSecretStore { return totpStore.Swap(s) }
func (s *TOTPSecretStore) identity(userID, recordID int) credentialcrypto.Identity {
	return credentialcrypto.Identity{Environment: s.Environment, TenantID: s.Namespace, OwnerType: "user", OwnerID: strconv.Itoa(userID), CredentialID: strconv.Itoa(recordID), Provider: "totp", CredentialType: "totp_seed", Purpose: "authentication", Version: 1}
}
func (s *TOTPSecretStore) valid() bool {
	return s != nil && s.Provider != nil && s.Namespace != "" && len(s.Namespace) <= 64 && (s.Environment == "test" || s.Environment == "development" || s.Environment == "production")
}
func (s *TOTPSecretStore) Seal(ctx context.Context, userID, recordID int, secret []byte) (string, error) {
	if !s.valid() || userID <= 0 || recordID <= 0 || len(secret) == 0 || len(secret) > 256 || !validLegacyTOTP(string(secret)) {
		return "", ErrTOTPStorage
	}
	e, err := credentialcrypto.Seal(ctx, s.Provider, "totp-local-v1", s.identity(userID, recordID), secret)
	if err != nil {
		return "", ErrTOTPStorage
	}
	b, err := common.Marshal(e)
	if err != nil {
		return "", ErrTOTPStorage
	}
	return totpEnvelopePrefix + string(b), nil
}
func (s *TOTPSecretStore) Open(ctx context.Context, row TwoFA) ([]byte, error) {
	if !s.valid() || row.Id <= 0 || row.UserId <= 0 || row.SecretFormat != 1 || !strings.HasPrefix(row.Secret, totpEnvelopePrefix) || len(row.Secret) > 8192 {
		return nil, ErrTOTPStorage
	}
	var e credentialcrypto.Envelope
	if common.UnmarshalJsonStr(strings.TrimPrefix(row.Secret, totpEnvelopePrefix), &e) != nil {
		return nil, ErrTOTPStorage
	}
	seed, err := credentialcrypto.Open(ctx, s.Provider, "totp-local-v1", s.identity(row.UserId, row.Id), e)
	if err != nil || len(seed) == 0 || len(seed) > 256 || !validLegacyTOTP(string(seed)) {
		clear(seed)
		return nil, ErrTOTPStorage
	}
	return seed, nil
}

// DecryptTOTPSecret must only be called after the operation's existing trusted
// session/challenge, ownership and factor checks. Never serialize its result.
func (t *TwoFA) DecryptTOTPSecret(ctx context.Context) ([]byte, error) {
	return totpStore.Load().Open(ctx, *t)
}

// BeforeCreate removes the seed before GORM builds INSERT parameters. AfterCreate
// seals against the database-assigned ID in the SAME transaction. No plaintext
// INSERT, no observable committed placeholder and no nontransactional fallback.
func (t *TwoFA) BeforeCreate(tx *gorm.DB) error {
	seed := []byte(t.Secret)
	t.Secret = ""
	if t.SecretFormat != 0 || len(seed) == 0 || !totpStore.Load().valid() {
		clear(seed)
		return ErrTOTPStorage
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		clear(seed)
		return ErrTOTPStorage
	}
	t.seedForCreate = seed
	return nil
}
func (t *TwoFA) AfterCreate(tx *gorm.DB) error {
	defer func() { clear(t.seedForCreate); t.seedForCreate = nil }()
	secret, err := totpStore.Load().Seal(tx.Statement.Context, t.UserId, t.Id, t.seedForCreate)
	if err != nil {
		return ErrTOTPStorage
	}
	result := tx.Session(&gorm.Session{SkipHooks: true, NewDB: true}).Model(&TwoFA{}).Where("id = ? AND user_id = ? AND secret = ? AND secret_format = ?", t.Id, t.UserId, "", 0).UpdateColumns(map[string]any{"secret": secret, "secret_format": 1})
	if result.Error != nil || result.RowsAffected != 1 {
		return ErrTOTPStorage
	}

	t.Secret, t.SecretFormat = secret, 1
	return nil
}

type TOTPStorageState struct {
	ID          int    `gorm:"primaryKey;autoIncrement:false"`
	Namespace   string `gorm:"size:64;not null"`
	Environment string `gorm:"size:16;not null"`
	Ready       bool   `gorm:"not null"`
	Version     uint32 `gorm:"not null"`
}

func TOTPReady(db *gorm.DB, s *TOTPSecretStore) error {
	if !s.valid() || !db.Migrator().HasTable(&TOTPStorageState{}) {
		return ErrTOTPStorage
	}
	var state TOTPStorageState
	if db.First(&state, 1).Error != nil || !state.Ready || state.Version != 1 || state.Namespace != s.Namespace || state.Environment != s.Environment {
		return ErrTOTPStorage
	}
	return VerifyTOTPRecords(db, s)
}
func VerifyTOTPRecords(db *gorm.DB, s *TOTPSecretStore) error {
	if !s.valid() {
		return ErrTOTPStorage
	}
	return db.Unscoped().Model(&TwoFA{}).FindInBatches(&[]TwoFA{}, 100, func(tx *gorm.DB, _ int) error {
		rows, ok := tx.Statement.Dest.(*[]TwoFA)
		if !ok {
			return ErrTOTPStorage
		}
		for _, row := range *rows {
			seed, err := s.Open(context.Background(), row)
			clear(seed)
			if err != nil {
				return ErrTOTPStorage
			}
		}
		return nil
	}).Error
}

type TOTPInventory struct{ Plaintext, Encrypted, Anomalous, Active, Pending, Deleted int }

func InventoryTOTP(db *gorm.DB, s *TOTPSecretStore) (TOTPInventory, error) {
	var rows []TwoFA
	var result TOTPInventory
	if db.Unscoped().Find(&rows).Error != nil {
		return result, ErrTOTPStorage
	}
	for _, row := range rows {
		if row.DeletedAt.Valid {
			result.Deleted++
		}
		if row.IsEnabled {
			result.Active++
		} else {
			result.Pending++
		}
		switch {
		case row.SecretFormat == 0 && validLegacyTOTP(row.Secret):
			result.Plaintext++
		case row.SecretFormat == 1:
			seed, err := s.Open(context.Background(), row)
			clear(seed)
			if err != nil {
				result.Anomalous++
			} else {
				result.Encrypted++
			}
		default:
			result.Anomalous++
		}
	}
	return result, nil
}
func validLegacyTOTP(seed string) bool {
	if len(seed) < 16 || len(seed) > 128 || strings.TrimSpace(seed) != seed || strings.ToUpper(seed) != seed {
		return false
	}
	b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(seed)
	ok := err == nil && len(b) >= 10
	clear(b)
	return ok
}

// MigrateTOTP runs only on an offline staged destination, never on a live input.
// Each row is atomic and CAS fenced. Completed rows survive interruption; READY
// is intentionally published separately AFTER independent restore verification.
func MigrateTOTP(db *gorm.DB, s *TOTPSecretStore, afterRow func(int) error) error {
	db = db.Session(&gorm.Session{Logger: gormlogger.Discard})
	if !s.valid() {
		return ErrTOTPStorage
	}
	if err := db.AutoMigrate(&TOTPStorageState{}); err != nil {
		return ErrTOTPStorage
	}
	var previous TOTPStorageState
	err := db.First(&previous, 1).Error
	if err == nil && (previous.Namespace != s.Namespace || previous.Environment != s.Environment || previous.Version != 1) {
		return ErrTOTPStorage
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrTOTPStorage
	}
	state := TOTPStorageState{ID: 1, Namespace: s.Namespace, Environment: s.Environment, Version: 1, Ready: false}
	if db.Save(&state).Error != nil {
		return ErrTOTPStorage
	}
	var ids []int
	if db.Unscoped().Model(&TwoFA{}).Order("id").Pluck("id", &ids).Error != nil {
		return ErrTOTPStorage
	}
	for _, id := range ids {
		err = db.Transaction(func(tx *gorm.DB) error {
			var row TwoFA
			if lockForUpdate(tx.Unscoped()).First(&row, id).Error != nil {
				return ErrTOTPStorage
			}
			if row.SecretFormat == 1 {
				seed, e := s.Open(context.Background(), row)
				clear(seed)
				return e
			}
			if row.SecretFormat != 0 || !validLegacyTOTP(row.Secret) {
				return ErrTOTPStorage
			}
			encrypted, e := s.Seal(context.Background(), row.UserId, row.Id, []byte(row.Secret))
			if e != nil {
				return e
			}
			check := row
			check.Secret, check.SecretFormat = encrypted, 1
			seed, e := s.Open(context.Background(), check)
			equal := e == nil && subtle.ConstantTimeCompare(seed, []byte(row.Secret)) == 1
			clear(seed)
			if !equal {
				return ErrTOTPStorage
			}
			result := tx.Unscoped().Model(&TwoFA{}).Where("id = ? AND user_id = ? AND secret = ? AND secret_format = ?", row.Id, row.UserId, row.Secret, 0).UpdateColumns(map[string]any{"secret": encrypted, "secret_format": 1})
			if result.Error != nil || result.RowsAffected != 1 {
				return ErrTOTPStorage
			}
			return nil
		})
		if err != nil {
			return ErrTOTPStorage
		}
		if afterRow != nil {
			if err = afterRow(id); err != nil {
				return err
			}
		}
	}
	return VerifyTOTPRecords(db, s)
}
func PublishTOTPReady(db *gorm.DB, s *TOTPSecretStore) error {
	if CheckTOTPStage(db, s) != nil {
		return ErrTOTPStorage
	}
	var state TOTPStorageState
	if db.First(&state, 1).Error != nil {
		return ErrTOTPStorage
	}

	if err := VerifyTOTPRecords(db, s); err != nil {
		return err
	}
	if state.Ready {
		return nil
	}
	result := db.Model(&TOTPStorageState{}).Where("id = ? AND namespace = ? AND environment = ? AND version = ?", 1, s.Namespace, s.Environment, 1).Update("ready", true)
	if result.Error != nil || result.RowsAffected != 1 {
		return ErrTOTPStorage
	}
	return nil
}

func RewrapTOTP(db *gorm.DB, s *TOTPSecretStore) error {
	var rows []TwoFA
	if db.Unscoped().Find(&rows).Error != nil {
		return ErrTOTPStorage
	}
	for _, row := range rows {
		seed, err := s.Open(context.Background(), row)
		if err != nil {
			return err
		}
		var envelope credentialcrypto.Envelope
		if common.UnmarshalJsonStr(strings.TrimPrefix(row.Secret, totpEnvelopePrefix), &envelope) != nil {
			clear(seed)
			return ErrTOTPStorage
		}
		wrapped, err := credentialcrypto.Rewrap(context.Background(), s.Provider, s.identity(row.UserId, row.Id), envelope)
		if err != nil {
			clear(seed)
			return ErrTOTPStorage
		}
		b, err := common.Marshal(wrapped)
		if err != nil {
			clear(seed)
			return ErrTOTPStorage
		}
		check := row
		check.Secret = totpEnvelopePrefix + string(b)
		plain, err := s.Open(context.Background(), check)
		equal := err == nil && subtle.ConstantTimeCompare(seed, plain) == 1
		clear(seed)
		clear(plain)
		if !equal {
			return ErrTOTPStorage
		}
		result := db.Unscoped().Model(&TwoFA{}).Where("id = ? AND user_id = ? AND secret = ? AND secret_format = ?", row.Id, row.UserId, row.Secret, 1).UpdateColumn("secret", check.Secret)
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrTOTPStorage
		}
	}
	return nil
}

func CheckTOTPStage(db *gorm.DB, s *TOTPSecretStore) error {
	var state TOTPStorageState
	if !s.valid() || !db.Migrator().HasTable(&state) || db.First(&state, 1).Error != nil || state.Version != 1 || state.Namespace != s.Namespace || state.Environment != s.Environment {
		return ErrTOTPStorage
	}
	return nil
}

func TOTPStorageEnvironment() string {
	s := totpStore.Load()
	if s == nil {
		return ""
	}
	return s.Environment
}
