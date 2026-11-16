// Package testchannel supplies real channel rows to outbound transport fixtures.
package testchannel

import (
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Bind gives a transport fixture an authoritative enabled channel. It never
// bypasses the production status gate or overwrites an existing channel's state.
func Bind(t testing.TB, info *relaycommon.RelayInfo, contexts ...*gin.Context) {
	t.Helper()
	require.NotNil(t, info)
	if info.ChannelMeta == nil {
		info.ChannelMeta = &relaycommon.ChannelMeta{}
	}
	defer func() {
		for _, c := range contexts {
			common.SetContextKey(c, constant.ContextKeyChannelId, info.ChannelId)
		}
	}()
	usable := false
	if model.DB != nil {
		if conn, err := model.DB.DB(); err == nil {
			usable = conn.Ping() == nil
		}
	}
	if !usable {
		previous, previousLog := model.DB, model.LOG_DB
		db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "channels.sqlite")), &gorm.Config{})
		require.NoError(t, err)
		conn, err := db.DB()
		require.NoError(t, err)
		model.DB, model.LOG_DB = db, db
		t.Cleanup(func() { model.DB, model.LOG_DB = previous, previousLog; require.NoError(t, conn.Close()) })
	}
	db := model.DB
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	var selected model.Channel
	if info.ChannelId > 0 {
		err := db.First(&selected, info.ChannelId).Error
		if err == nil {
			require.Equal(t, common.ChannelStatusEnabled, selected.Status)
			return
		}
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	}
	selected = model.Channel{Id: info.ChannelId, Status: common.ChannelStatusEnabled, Name: "synthetic-outbound-fixture"}
	require.NoError(t, db.Create(&selected).Error)
	info.ChannelId = selected.Id
	t.Cleanup(func() { require.NoError(t, db.Delete(&model.Channel{}, selected.Id).Error) })
}
