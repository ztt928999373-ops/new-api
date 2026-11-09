// Copyright (c) QuantumNous and contributors.
package controller

import (
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestSetupTransactionRollbackAndConcurrentWinner(t *testing.T) {
	for _, failTable := range []string{"users", "options", "setups", "none"} {
		t.Run(failTable, func(t *testing.T) {
			oldDB, oldSetup, oldOptions := model.DB, constant.Setup, common.OptionMap
			oldSelf, oldDemo := operation_setting.SelfUseModeEnabled, operation_setting.DemoSiteEnabled
			t.Cleanup(func() {
				model.DB, constant.Setup, common.OptionMap = oldDB, oldSetup, oldOptions
				operation_setting.SelfUseModeEnabled, operation_setting.DemoSiteEnabled = oldSelf, oldDemo
			})
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "setup.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { sqlDB.Close() })
			sqlDB.SetMaxOpenConns(1)
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Option{}, &model.Setup{}))
			model.DB = db
			constant.Setup = false
			common.OptionMap = map[string]string{}
			operation_setting.SelfUseModeEnabled = false
			operation_setting.DemoSiteEnabled = false
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:setup-failure", func(tx *gorm.DB) {
				if tx.Statement.Table == failTable {
					tx.AddError(errors.New("synthetic setup write fault"))
				}
			}))
			call := func() *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest("POST", "/api/setup", strings.NewReader(`{"username":"batchc-root","password":"Synthetic-Setup-Password!","confirmPassword":"Synthetic-Setup-Password!","SelfUseModeEnabled":true}`))
				c.Request.Header.Set("Content-Type", "application/json")
				PostSetup(c)
				return w
			}
			if failTable != "none" {
				response := call()
				require.Contains(t, response.Body.String(), `"success":false`)
				for _, table := range []any{&model.User{}, &model.Option{}, &model.Setup{}} {
					var count int64
					require.NoError(t, db.Model(table).Count(&count).Error)
					require.Zero(t, count)
				}
				require.False(t, constant.Setup)
				require.False(t, operation_setting.SelfUseModeEnabled)
				require.Empty(t, common.OptionMap)
			} else {
				start := make(chan struct{})
				results := make(chan string, 2)
				var wg sync.WaitGroup
				for range 2 {
					wg.Go(func() { <-start; results <- call().Body.String() })
				}
				close(start)
				wg.Wait()
				close(results)
				wins := 0
				for response := range results {
					if strings.Contains(response, `"success":true`) {
						wins++
					}
				}
				require.Equal(t, 1, wins)
				var roots, setups int64
				require.NoError(t, db.Model(&model.User{}).Count(&roots).Error)
				require.NoError(t, db.Model(&model.Setup{}).Count(&setups).Error)
				require.EqualValues(t, 1, roots)
				require.EqualValues(t, 1, setups)
				require.True(t, constant.Setup)
			}
			require.NoError(t, db.Callback().Create().Remove("test:setup-failure"))
		})
	}
}
