// Copyright (c) QuantumNous and contributors.
package credentialcrypto

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrivateSnapshotNeverOverwritesAndHonorsWriterLock(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	backup := filepath.Join(root, "backup")
	require.NoError(t, InitializeLocalDirectory(source))
	require.NoError(t, InitializeLocalDirectory(backup))
	ring := filepath.Join(source, "ring")
	require.NoError(t, InitializeLocalFile(ring))
	p, err := NewLocalFileKeyProvider(ring)
	require.NoError(t, err)
	file := filepath.Join(backup, "snapshot")
	require.NoError(t, p.SnapshotTo(file))
	require.NoError(t, ValidatePrivateArtifact(file, false))
	before, err := os.ReadFile(file)
	require.NoError(t, err)
	defer clear(before)
	require.Error(t, p.SnapshotTo(file))
	after, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, before, after)
	clear(after)
	copy, err := NewLocalFileKeyProvider(file)
	require.NoError(t, err)
	original, err := p.GetKeyVersion(context.Background())
	require.NoError(t, err)
	copied, err := copy.GetKeyVersion(context.Background())
	require.NoError(t, err)
	require.Equal(t, original, copied)
	unlock, err := p.lock()
	require.NoError(t, err)
	blocked := filepath.Join(backup, "busy")
	require.Error(t, p.SnapshotTo(blocked))
	unlock()
	_, err = os.Stat(blocked)
	require.True(t, os.IsNotExist(err))
	require.NoError(t, p.SnapshotTo(blocked))
}
