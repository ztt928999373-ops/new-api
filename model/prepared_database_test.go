// Copyright (c) QuantumNous and contributors.
package model

import (
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestPreparedDatabaseOpenNeverMigrates(t *testing.T) {
	oldDB, oldLog, oldPath, oldMaster := DB, LOG_DB, common.SQLitePath, common.IsMasterNode
	oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	t.Cleanup(func() {
		DB, LOG_DB, common.SQLitePath, common.IsMasterNode = oldDB, oldLog, oldPath, oldMaster
		common.SetDatabaseTypes(oldMainType, oldLogType)
	})
	t.Setenv("SQL_DSN", "local")
	t.Setenv("LOG_SQL_DSN", "")
	common.SQLitePath = filepath.Join(t.TempDir(), "prepared.sqlite")
	common.IsMasterNode = true
	require.NoError(t, InitDB())
	require.NoError(t, InitLogDB())
	require.NoError(t, DB.Migrator().DropTable(&LoginEncryptionKey{}))
	require.NoError(t, DB.Migrator().DropTable(&AuditLog{}))
	require.NoError(t, CloseDB())
	require.NoError(t, OpenPreparedDB())
	require.NoError(t, OpenPreparedLogDB())
	require.False(t, DB.Migrator().HasTable(&LoginEncryptionKey{}))
	require.False(t, DB.Migrator().HasTable(&AuditLog{}))
	require.NoError(t, CloseDB())
	require.NoError(t, InitDB())
	require.NoError(t, InitLogDB())
	defer CloseDB()
	require.True(t, DB.Migrator().HasTable(&LoginEncryptionKey{}))
	require.True(t, DB.Migrator().HasTable(&AuditLog{}))
}
