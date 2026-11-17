// Copyright (c) QuantumNous and contributors.
//go:build !windows

package credentialcrypto

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockLocalFile(f *os.File) (func(), error) {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, err
	}
	return func() { unix.Flock(int(f.Fd()), unix.LOCK_UN); f.Close() }, nil
}
