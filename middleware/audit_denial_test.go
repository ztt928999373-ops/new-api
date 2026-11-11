package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func setupDenialAuditTest(t *testing.T) {
	t.Helper()
	setupDashboardAuthMiddlewareTest(t)
	sqlDB, err := model.DB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	previous := dashboardDenialBudget
	dashboardDenialBudget = &denialAuditBudget{inflight: make(chan struct{}, 2)}
	t.Cleanup(func() { dashboardDenialBudget = previous })
}

func TestDashboardDenialAuditAuthenticationAndRedaction(t *testing.T) {
	for _, scenario := range []string{"anonymous", "invalid", "expired", "revoked", "role", "permission", "scope", "authorized"} {
		t.Run(scenario, func(t *testing.T) {
			setupDenialAuditTest(t)
			const secret = "F11-sensitive-original"
			role := common.RoleCommonUser
			if scenario == "permission" || scenario == "scope" {
				role = common.RoleAdminUser
			}
			if scenario == "authorized" {
				role = common.RoleRootUser
			}
			u := model.User{Username: secret, Role: role, Status: common.UserStatusEnabled, AuthVersion: 1, AffCode: "f11"}
			require.NoError(t, model.DB.Create(&u).Error)
			bundle, err := service.CreateLoginSession(u.Id, "synthetic", "127.0.0.1", "synthetic")
			require.NoError(t, err)
			token := bundle.AccessToken
			want := http.StatusUnauthorized
			switch scenario {
			case "anonymous":
				token = ""
			case "invalid":
				token = secret
			case "expired":
				identity, err := service.ParseAccessToken(token)
				require.NoError(t, err)
				token = issueExpiredDashboardAccessToken(t, identity)
			case "revoked":
				require.NoError(t, service.RevokeByRefreshToken(bundle.RefreshToken, bundle.Session.SID, "synthetic"))
			case "scope":
				token, _ = createMiddlewareScopedToken(t, u.Id, 0, "profile:read")
				want = http.StatusForbidden
			case "role", "permission":
				want = http.StatusForbidden
			case "authorized":
				want = http.StatusNoContent
			}
			r := gin.New()
			r.Use(RequestId())
			handlers := []gin.HandlerFunc{AdminAuth()}
			if scenario == "permission" {
				require.NoError(t, model.DB.AutoMigrate(&model.CasbinRule{}, &model.AuthzRole{}))
				require.NoError(t, authz.Init(model.DB))
				handlers = append(handlers, RequirePermission(authz.ChannelSensitiveWrite))
			}
			reached := false
			handlers = append(handlers, func(c *gin.Context) { reached = true; c.Status(204) })
			r.POST("/restricted/:id", handlers...)
			req := httptest.NewRequest("POST", "/restricted/"+secret+"?api_key="+secret, strings.NewReader(`{"password":"`+secret+`"}`))
			req.Header.Set("Cookie", "secret="+secret)
			req.Header.Set("User-Agent", secret)
			req.Header.Set("X-API-Key", secret)
			req.Header.Set(common.RequestIdKey, secret)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, want, w.Code, w.Body.String())
			require.Equal(t, scenario == "authorized", reached)
			var rows []model.AuditLog
			require.NoError(t, model.DB.Where("action = ?", "auth.denied").Find(&rows).Error)
			if scenario == "authorized" {
				require.Empty(t, rows)
				var success model.AuditLog
				require.NoError(t, model.DB.Where("request_id = ?", w.Header().Get(common.RequestIdKey)).First(&success).Error)
				require.True(t, success.Success)
				return
			}
			require.Len(t, rows, 1)
			e := rows[0]
			assert.Equal(t, want, e.Status)
			assert.False(t, e.Success)
			if scenario == "scope" {
				assert.Equal(t, "access_token", e.Category)
			} else {
				assert.Equal(t, "security", e.Category)
			}
			assert.Equal(t, "/restricted/:id", e.Route)
			assert.Equal(t, "POST", e.Method)
			assert.Equal(t, w.Header().Get(common.RequestIdKey), e.RequestId)
			assert.NotEmpty(t, e.EventId)
			assert.Positive(t, e.CreatedAt)
			if scenario == "role" || scenario == "permission" || scenario == "scope" {
				assert.Equal(t, u.Id, e.UserId)
				assert.Equal(t, role, e.ActorRole)
			} else {
				assert.Zero(t, e.UserId)
				assert.Zero(t, e.ActorRole)
			}
			encoded, err := common.Marshal(e)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), secret)
			assert.NotContains(t, string(encoded), bundle.AccessToken)
			assert.NotContains(t, string(encoded), bundle.RefreshToken)
			assert.Empty(t, e.UserAgent)
			if scenario == "scope" {
				assert.Equal(t, model.AccessTokenFingerprint(token), e.TokenRef)
			} else {
				assert.Empty(t, e.TokenRef)
			}
			var count int64
			require.NoError(t, model.DB.Model(&model.AuditLog{}).Where("request_id = ?", e.RequestId).Count(&count).Error)
			require.EqualValues(t, 1, count, "one bounded denial event, no duplicate operation/PAT writer")
		})
	}
}

func TestDashboardDenialAuditDatabaseMatrix(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			var dialect gorm.Dialector = sqlite.Open(":memory:")
			dbType := common.DatabaseTypeSQLite
			if kind != "sqlite" {
				if os.Getenv("SUANJIE_TEST_DATABASES") != "local-synthetic" {
					t.Skip("isolated synthetic database not configured")
				}
				if kind == "mysql" {
					dialect = mysql.Open("root:synthetic-test-only@tcp(127.0.0.1:23316)/credential_test?parseTime=true")
					dbType = common.DatabaseTypeMySQL
				} else {
					dialect = postgres.Open("host=127.0.0.1 port=25436 user=postgres password=synthetic-test-only dbname=credential_test sslmode=disable")
					dbType = common.DatabaseTypePostgreSQL
				}
			}
			db, err := gorm.Open(dialect, &gorm.Config{})
			require.NoError(t, err)
			versionSQL := "SELECT VERSION()"
			if kind == "sqlite" {
				versionSQL = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(versionSQL).Scan(&version).Error)
			t.Logf("audit database=%s engine=%s", kind, version)
			conn, err := db.DB()
			require.NoError(t, err)
			defer conn.Close()
			require.NoError(t, db.AutoMigrate(&model.AuditLog{}))
			previousDB, previousLog := model.DB, model.LOG_DB
			previousMain, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
			previousBudget := dashboardDenialBudget
			defer func() {
				model.DB, model.LOG_DB = previousDB, previousLog
				common.SetDatabaseTypes(previousMain, previousLogType)
				dashboardDenialBudget = previousBudget
			}()
			model.DB, model.LOG_DB = db, db
			common.SetDatabaseTypes(dbType, dbType)
			dashboardDenialBudget = &denialAuditBudget{inflight: make(chan struct{}, 2)}
			r := gin.New()
			r.Use(RequestId())
			r.GET("/restricted", AdminAuth(), func(c *gin.Context) { c.Status(204) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/restricted", nil))
			require.Equal(t, 401, w.Code)
			var row model.AuditLog
			id := w.Header().Get(common.RequestIdKey)
			require.NoError(t, db.Where("request_id = ?", id).First(&row).Error)
			defer db.Where("request_id = ?", id).Delete(&model.AuditLog{})
			require.Equal(t, "auth.denied", row.Action)
			require.Equal(t, 401, row.Status)
			require.False(t, row.Success)
			require.NotNil(t, row.Other.Op)
		})
	}
}

func TestDashboardDenialAuditBurstAndConcurrentRequests(t *testing.T) {
	setupDenialAuditTest(t)
	r := gin.New()
	r.Use(RequestId())
	r.GET("/restricted", AdminAuth(), func(c *gin.Context) { c.Status(204) })
	// The first window admits at most 60 attempts, then aggregates suppression.
	for range 61 {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/restricted", nil))
		require.Equal(t, 401, w.Code)
	}
	var count int64
	require.NoError(t, model.DB.Model(&model.AuditLog{}).Where("action = ?", "auth.denied").Count(&count).Error)
	require.EqualValues(t, 60, count)
	require.EqualValues(t, 1, dashboardDenialBudget.suppressed.Load())
	dashboardDenialBudget.window = time.Now().Add(-time.Minute)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/restricted", nil))
	var latest model.AuditLog
	require.NoError(t, model.DB.Order("id DESC").First(&latest).Error)
	encoded, err := common.Marshal(latest.Other)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"suppressed_events":1`)

	// Concurrent denials keep their original response; overload cannot authorize.
	var wg sync.WaitGroup
	statuses := make(chan int, 16)
	for range 16 {
		wg.Go(func() {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/restricted", nil))
			statuses <- w.Code
		})
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		require.Equal(t, 401, status)
	}
	var rows []model.AuditLog
	require.NoError(t, model.DB.Where("action = ?", "auth.denied").Find(&rows).Error)
	ids := map[string]bool{}
	for _, row := range rows {
		require.False(t, ids[row.RequestId])
		ids[row.RequestId] = true
	}
	require.LessOrEqual(t, len(rows), 77)
	require.Zero(t, dashboardDenialBudget.failed.Load())
	accounted := uint64(len(rows)) + dashboardDenialBudget.suppressed.Load()
	for _, row := range rows {
		var summary struct {
			Suppressed uint64 `json:"suppressed_events"`
		}
		data, err := common.Marshal(row.Other.Op.Params)
		require.NoError(t, err)
		require.NoError(t, common.Unmarshal(data, &summary))
		accounted += summary.Suppressed
	}
	require.EqualValues(t, 78, accounted, "persisted and suppressed events must all be accounted for")
}

func TestDashboardDenialAuditInflightBound(t *testing.T) {
	setupDenialAuditTest(t)
	// Occupy both writer slots without involving timing or a blocked database.
	dashboardDenialBudget.inflight <- struct{}{}
	dashboardDenialBudget.inflight <- struct{}{}
	r := gin.New()
	r.GET("/restricted", AdminAuth(), func(c *gin.Context) { c.Status(204) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/restricted", nil))
	require.Equal(t, 401, w.Code)
	require.EqualValues(t, 1, dashboardDenialBudget.suppressed.Load())
	var count int64
	require.NoError(t, model.DB.Model(&model.AuditLog{}).Count(&count).Error)
	require.Zero(t, count)
	<-dashboardDenialBudget.inflight
	<-dashboardDenialBudget.inflight
}

func TestDashboardDenialAuditFailureAndDeadline(t *testing.T) {
	setupDenialAuditTest(t)
	r := gin.New()
	r.GET("/restricted", AdminAuth(), func(c *gin.Context) { c.Status(204) })
	db := model.LOG_DB
	model.LOG_DB = nil
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/restricted", nil))
	require.Equal(t, 401, w.Code)
	require.EqualValues(t, 1, dashboardDenialBudget.failed.Load())
	model.LOG_DB = db
	t.Cleanup(func() { model.LOG_DB = db })
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/restricted", nil))
	var latest model.AuditLog
	require.NoError(t, db.Order("id DESC").First(&latest).Error)
	encoded, err := common.Marshal(latest.Other)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"failed_events":1`)
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("f11-deadline", func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_logs" {
			<-tx.Statement.Context.Done()
			tx.AddError(context.DeadlineExceeded)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove("f11-deadline") })
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/restricted", nil))
	require.Equal(t, 401, w.Code)
	require.EqualValues(t, 1, dashboardDenialBudget.failed.Load())
}

func TestDashboardDenialAuditPreservesDisconnectedOperationAudit(t *testing.T) {
	setupDenialAuditTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/operation", nil).WithContext(ctx)
	model.RecordAuditLog(c, model.AuditLog{Action: "synthetic.operation", Username: "synthetic", ActorRole: common.RoleRootUser, Success: true})
	var count int64
	require.NoError(t, model.DB.Model(&model.AuditLog{}).Where("action = ?", "synthetic.operation").Count(&count).Error)
	require.EqualValues(t, 1, count)
}
