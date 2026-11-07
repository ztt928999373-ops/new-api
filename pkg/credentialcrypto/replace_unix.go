// Copyright (c) QuantumNous and contributors.
//go:build !windows

package credentialcrypto

import (
	"os"
	"path/filepath"
)

func durableReplace(source, target string) error {
	if os.Rename(source, target) != nil {
		return ErrUnavailable
	}
	dir, err := os.Open(filepath.Dir(target))
	if err != nil {
		return ErrUnavailable
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrUnavailable
	}
	return nil
}
