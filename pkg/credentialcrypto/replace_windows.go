// Copyright (c) QuantumNous and contributors.
//go:build windows

package credentialcrypto

import "golang.org/x/sys/windows"

func durableReplace(source, target string) error {
	s, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return ErrUnavailable
	}
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return ErrUnavailable
	}
	if windows.MoveFileEx(s, t, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH) != nil {
		return ErrUnavailable
	}
	return nil
}
