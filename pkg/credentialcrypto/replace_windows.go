// Copyright (c) QuantumNous and contributors.
//go:build windows

package credentialcrypto

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func currentSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", ErrUnavailable
	}
	return u.User.Sid.String(), nil
}

func privateSecurity(path string) (*windows.SECURITY_DESCRIPTOR, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || !sd.IsValid() {
		return nil, ErrUnavailable
	}
	return sd, nil
}

// Protected DACLs are checked separately. Windows retains AUTO_INHERITED
// provenance on temporary files after SetNamedSecurityInfo; it does not grant
// access. Compare the owner and actual ACEs, not that provenance bit.
func privatePermissions(sd *windows.SECURITY_DESCRIPTOR) string {
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return ""
	}
	_, dacl, ok := strings.Cut(sd.String(), "D:")
	if !ok {
		return ""
	}
	_, entries, ok := strings.Cut(dacl, "(")
	if !ok {
		return ""
	}
	return owner.String() + "|(" + entries
}

func trustedLocalSID(sid *windows.SID, user string) bool {
	if sid == nil || !sid.IsValid() {
		return false
	}
	value := sid.String()
	return value == user || value == "S-1-5-18" || value == "S-1-5-32-544"
}

// A writable ancestor can be used to replace/roll back an otherwise private
// directory. Read/traverse/create-subdirectory grants on OS roots are allowed;
// untrusted delete-child, reparse/write-data and ACL/owner control are not.
func validatePrivateAncestor(path, user string) error {
	sd, err := privateSecurity(path)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	// Windows volume roots are normally owned by TrustedInstaller. This
	// exception applies only to ancestors, never to the secret directory/ring.
	const trustedInstaller = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
	if err != nil || owner == nil || (!trustedLocalSID(owner, user) && owner.String() != trustedInstaller) {
		return ErrUnavailable
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return ErrUnavailable
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(dacl, i, &ace) != nil || ace == nil {
			return ErrUnavailable
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			const fileDeleteChild = 0x00000040 // FILE_DELETE_CHILD, winnt.h
			const unsafeRights = windows.GENERIC_ALL | windows.GENERIC_WRITE | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | fileDeleteChild | windows.FILE_WRITE_DATA
			if uint32(ace.Mask)&unsafeRights != 0 && !trustedLocalSID((*windows.SID)(unsafe.Pointer(&ace.SidStart)), user) {
				return ErrUnavailable
			}
		case windows.ACCESS_DENIED_ACE_TYPE:
		default:
			return ErrUnavailable
		}
	}
	return nil
}

func localWindowsPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil || strings.HasPrefix(filepath.VolumeName(abs), `\\`) {
		return "", ErrUnavailable
	}
	root, err := windows.UTF16PtrFromString(filepath.VolumeName(abs) + `\`)
	if err != nil {
		return "", ErrUnavailable
	}
	drive := windows.GetDriveType(root)
	if drive != windows.DRIVE_FIXED && drive != windows.DRIVE_RAMDISK {
		return "", ErrUnavailable
	}
	return abs, nil
}

func validatePrivatePath(path string, directory bool) error {
	abs, err := localWindowsPath(path)
	if err != nil {
		return ErrUnavailable
	}
	user, err := currentSID()
	if err != nil {
		return err
	}
	// Reject junctions/reparse points in the path, not just the final ring.
	for component := abs; ; component = filepath.Dir(component) {
		p, err := windows.UTF16PtrFromString(component)
		if err != nil {
			return ErrUnavailable
		}
		attrs, err := windows.GetFileAttributes(p)
		if err != nil || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return ErrUnavailable
		}
		if component != abs && validatePrivateAncestor(component, user) != nil {
			return ErrUnavailable
		}
		if filepath.Dir(component) == component {
			break
		}
	}
	info, err := os.Lstat(abs)
	if err != nil || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) {
		return ErrUnavailable
	}
	sd, err := privateSecurity(abs)
	if err != nil {
		return err
	}
	trusted := func(sid *windows.SID) bool {
		return trustedLocalSID(sid, user)
	}
	owner, _, err := sd.Owner()
	if err != nil || !trusted(owner) {
		return ErrUnavailable
	}
	control, _, err := sd.Control()
	if err != nil || (!directory && control&windows.SE_DACL_PROTECTED == 0) {
		return ErrUnavailable
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount == 0 {
		return ErrUnavailable
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(dacl, i, &ace) != nil || ace == nil {
			return ErrUnavailable
		}
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			if ace.Mask != 0 && !trusted((*windows.SID)(unsafe.Pointer(&ace.SidStart))) {
				return ErrUnavailable
			}
		case windows.ACCESS_DENIED_ACE_TYPE:
			// Denials do not broaden access.
		default:
			return ErrUnavailable // conditional/object ACEs require explicit review
		}
	}
	return nil
}

func createPrivateFile(path string) (*os.File, error) {
	user, err := currentSID()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + user + "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + user + ")")
	if err != nil {
		return nil, ErrUnavailable
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, windows.FILE_SHARE_READ, &sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	return os.NewFile(uintptr(h), path), nil
}

func createPrivateDirectory(path string) error {
	if _, err := localWindowsPath(path); err != nil {
		return err
	}
	user, err := currentSID()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + user + "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + user + ")")
	if err != nil {
		return ErrUnavailable
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ErrUnavailable
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	if windows.CreateDirectory(p, &sa) != nil {
		return ErrUnavailable
	}
	return validatePrivatePath(path, true)
}

func preservePrivatePermissions(path, target string) error {
	sd, err := privateSecurity(target)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return ErrUnavailable
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, dacl, nil) != nil {
		return ErrUnavailable
	}
	return nil
}

func durableReplace(source, target string) error {
	if validatePrivatePath(filepath.Dir(target), true) != nil || validatePrivatePath(source, false) != nil || validatePrivatePath(target, false) != nil {
		return ErrUnavailable
	}
	expected, err := privateSecurity(target)
	if err != nil {
		return err
	}
	actual, err := privateSecurity(source)
	if err != nil || privatePermissions(actual) == "" || privatePermissions(actual) != privatePermissions(expected) {
		return ErrUnavailable
	}
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
	actual, err = privateSecurity(target)
	if err != nil || privatePermissions(actual) != privatePermissions(expected) || validatePrivatePath(target, false) != nil {
		return ErrUnavailable
	}
	return nil
}
