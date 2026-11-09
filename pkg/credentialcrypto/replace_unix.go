// Copyright (c) QuantumNous and contributors.
//go:build !windows

package credentialcrypto

import (
	"os"
	"path/filepath"
)

func validatePrivatePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) || info.Mode().Perm()&0077 != 0 {
		return ErrUnavailable
	}
	return nil
}

func createPrivateFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}

func createPrivateDirectory(path string) error {
	if os.Mkdir(path, 0700) != nil {
		return ErrUnavailable
	}
	return validatePrivatePath(path, true)
}

func preservePrivatePermissions(path, target string) error {
	info, err := os.Stat(target)
	if err != nil || os.Chmod(path, info.Mode().Perm()) != nil {
		return ErrUnavailable
	}
	return nil
}

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
