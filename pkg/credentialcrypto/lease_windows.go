// Copyright (c) QuantumNous and contributors.
//go:build windows

package credentialcrypto

import (
	"golang.org/x/sys/windows"
	"os"
)

func lockLocalFile(f *os.File) (func(), error) {
	overlap := &windows.Overlapped{}
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlap); err != nil {
		return nil, err
	}
	return func() { windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, overlap); f.Close() }, nil
}
