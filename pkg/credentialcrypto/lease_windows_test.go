// Copyright (c) QuantumNous and contributors.
//go:build windows

package credentialcrypto

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreatePrivateFileExistingPathPreservesIsExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")

	file, err := createPrivateFile(path)
	require.NoError(t, err)
	require.NoError(t, file.Close())

	file, err = createPrivateFile(path)
	require.Nil(t, file)
	require.Error(t, err)
	require.True(t, os.IsExist(err), "CREATE_NEW existing-file errors must remain detectable")
	require.NoError(t, validatePrivatePath(path, false))
}
