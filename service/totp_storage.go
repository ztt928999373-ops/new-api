// Copyright (c) QuantumNous and contributors.
package service

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/QuantumNous/new-api/internal/totpops"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

var totpRuntime struct {
	sync.Mutex
	release func()
}

// InitTOTPStorage never initializes keys or migrates plaintext. The opt-in is
// single-instance/local only, independently from supplier credential wiring.
func InitTOTPStorage(db *gorm.DB) error {
	dir := os.Getenv("TOTP_KEY_DIRECTORY")
	if dir == "" {
		var count int64
		if db.Unscoped().Model(&model.TwoFA{}).Count(&count).Error != nil || count != 0 {
			return model.ErrTOTPStorage
		}
		if db.Migrator().HasTable(&model.TOTPStorageState{}) {
			var protected int64
			if db.Model(&model.TOTPStorageState{}).Count(&protected).Error != nil || protected != 0 {
				return model.ErrTOTPStorage
			}
		}
		model.SetTOTPSecretStore(nil)
		return nil
	}
	if !filepath.IsAbs(dir) || os.Getenv("TOTP_DEPLOYMENT_MODE") != "single-instance-local" || (os.Getenv("NODE_TYPE") != "" && os.Getenv("NODE_TYPE") != "master") {
		return model.ErrTOTPStorage
	}
	s, _, err := totpops.OpenKeys(dir)
	if err != nil || s.Environment != os.Getenv("TOTP_ENVIRONMENT") {
		return model.ErrTOTPStorage
	}
	if s.Environment != "production" && os.Getenv("BIND_ADDRESS") != "127.0.0.1" && os.Getenv("BIND_ADDRESS") != "::1" {
		return model.ErrTOTPStorage
	}
	release, err := totpops.Acquire(dir)
	if err != nil {
		return model.ErrTOTPStorage
	}
	if err = model.TOTPReady(db, s); err != nil {
		release()
		return err
	}
	totpRuntime.Lock()
	defer totpRuntime.Unlock()
	if totpRuntime.release != nil {
		release()
		return model.ErrTOTPStorage
	}
	totpRuntime.release = release
	model.SetTOTPSecretStore(s)
	return nil
}
func CloseTOTPStorage() {
	totpRuntime.Lock()
	defer totpRuntime.Unlock()
	model.SetTOTPSecretStore(nil)
	if totpRuntime.release != nil {
		totpRuntime.release()
		totpRuntime.release = nil
	}
}
