// Copyright (c) QuantumNous and contributors.
package credentialcrypto

import (
	"os"
	"path/filepath"
)

// AcquireLocalLease holds a private OS lock until release/process death. It is
// single-host only, never a distributed lock or permission to copy a keyring.
func AcquireLocalLease(path string) (func(), error) {
	if validatePrivatePath(filepath.Dir(path), true) != nil {
		return nil, ErrUnavailable
	}
	f, err := createPrivateFile(path)
	if os.IsExist(err) {
		if validatePrivatePath(path, false) != nil {
			return nil, ErrUnavailable
		}
		f, err = os.OpenFile(path, os.O_RDWR, 0)
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	info, err := f.Stat()
	named, e := os.Lstat(path)
	if err != nil || e != nil || !os.SameFile(info, named) || validatePrivatePath(path, false) != nil {
		f.Close()
		return nil, ErrUnavailable
	}
	release, err := lockLocalFile(f)
	if err != nil {
		f.Close()
		return nil, ErrUnavailable
	}
	return release, nil
}
