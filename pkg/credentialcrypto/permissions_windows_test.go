//go:build windows

package credentialcrypto

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestPrivateRingACLAndReplacement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, InitializeLocalDirectory(dir))
	path := filepath.Join(dir, "ring")
	require.NoError(t, InitializeLocalFile(path))
	p, err := NewLocalFileKeyProvider(path)
	require.NoError(t, err)
	tmp, err := os.CreateTemp(dir, "empty-acl-test-*")
	require.NoError(t, err)
	name := tmp.Name()
	require.NoError(t, tmp.Close())
	defer os.Remove(name)
	require.ErrorIs(t, validatePrivatePath(name, false), ErrUnavailable, "inherited temporary ACL must be protected before writing secrets")
	require.NoError(t, preservePrivatePermissions(name, path))
	require.NoError(t, validatePrivatePath(name, false))
	a, err := privateSecurity(path)
	require.NoError(t, err)
	b, err := privateSecurity(name)
	require.NoError(t, err)
	require.Equal(t, privatePermissions(a), privatePermissions(b))
	_, err = p.EncryptDEK(context.Background(), make([]byte, 32), []byte("synthetic-aad"))
	require.NoError(t, err)
	_, err = p.RotateKEK(context.Background())
	require.NoError(t, err)
	require.NoError(t, validatePrivatePath(path, false))
	after, err := privateSecurity(path)
	require.NoError(t, err)
	require.Equal(t, privatePermissions(a), privatePermissions(after), "rotation/replacement preserves the original effective ACL")
	unsafeACL, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	require.NoError(t, err)
	unsafeDACL, _, err := unsafeACL.DACL()
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(name, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, unsafeDACL, nil))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	defer clear(before)
	require.ErrorIs(t, durableReplace(name, path), ErrUnavailable, "unsafe temporary files cannot replace a trusted ring")
	unchanged, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, unchanged)
	clear(unchanged)
	// A NULL DACL grants everybody access and must be rejected too.
	require.NoError(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, nil, nil))
	_, err = p.GetKeyVersion(context.Background())
	require.ErrorIs(t, err, ErrUnavailable)
	originalDACL, _, err := a.DACL()
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, originalDACL, nil))
	for _, insecure := range []string{path, dir} {
		sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
		require.NoError(t, err)
		dacl, _, err := sd.DACL()
		require.NoError(t, err)
		require.NoError(t, windows.SetNamedSecurityInfo(insecure, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil))
		_, err = NewLocalFileKeyProvider(path)
		require.ErrorIs(t, err, ErrUnavailable)
		_, err = p.EncryptDEK(context.Background(), make([]byte, 32), []byte("synthetic-aad"))
		require.ErrorIs(t, err, ErrUnavailable, "an already constructed provider rechecks permissions")
		if insecure == path {
			dacl, _, err = a.DACL()
			require.NoError(t, err)
			require.NoError(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil))
		}
	}
}

func TestPrivateRingRejectsWritableAncestorAndNetworkPath(t *testing.T) {
	_, err := NewLocalFileKeyProvider(`\\synthetic.invalid\share\ring`)
	require.ErrorIs(t, err, ErrUnavailable, "network paths are rejected before any filesystem access")
	ancestor := filepath.Join(t.TempDir(), "ancestor")
	require.NoError(t, InitializeLocalDirectory(ancestor))
	dir := filepath.Join(ancestor, "keys")
	require.NoError(t, InitializeLocalDirectory(dir))
	path := filepath.Join(dir, "ring")
	require.NoError(t, InitializeLocalFile(path))
	p, err := NewLocalFileKeyProvider(path)
	require.NoError(t, err)
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	require.NoError(t, err)
	dacl, _, err := sd.DACL()
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(ancestor, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil))
	_, err = p.EncryptDEK(context.Background(), make([]byte, 32), []byte("synthetic-aad"))
	require.ErrorIs(t, err, ErrUnavailable)
	_, err = os.Stat(path + ".lock")
	require.True(t, os.IsNotExist(err), "unsafe ancestry is rejected before creating a lock or temporary ring")
	_, err = NewLocalFileKeyProvider(path)
	require.ErrorIs(t, err, ErrUnavailable)
}
