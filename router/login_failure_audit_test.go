package router

import (
	"errors"
	"fmt"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

func setupPasswordFailureAudit(t *testing.T, kind string) (*gin.Engine, model.User) {
	t.Helper()
	db := setupUpstreamBudgetDB(t, kind)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.TwoFA{}, &model.PasskeyCredential{}, &model.AuthFlow{}, &model.UserOAuthBinding{}, &model.Option{}, &model.UserAccessToken{}, &model.AuditLog{}))
	oldLogin, oldEncryption, oldTurnstile := common.PasswordLoginEnabled, common.PasswordLoginEncryptionEnabled, common.TurnstileCheckEnabled
	oldCritical, oldGlobal, oldSecret := common.CriticalRateLimitEnable, common.GlobalApiRateLimitEnable, common.SessionSecret
	t.Cleanup(func() {
		common.PasswordLoginEnabled, common.PasswordLoginEncryptionEnabled, common.TurnstileCheckEnabled = oldLogin, oldEncryption, oldTurnstile
		common.CriticalRateLimitEnable, common.GlobalApiRateLimitEnable, common.SessionSecret = oldCritical, oldGlobal, oldSecret
	})
	common.PasswordLoginEnabled, common.PasswordLoginEncryptionEnabled, common.TurnstileCheckEnabled = true, false, false
	common.CriticalRateLimitEnable, common.GlobalApiRateLimitEnable = false, false
	common.SessionSecret = "synthetic-login-audit-only"
	name := fmt.Sprintf("laf%x", time.Now().UnixNano())
	hash, err := common.HashAccountPassword("Synthetic-Audit-Only!2026")
	require.NoError(t, err)
	user := model.User{Username: name, AffCode: name, Password: hash, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, Group: "default"}
	require.NoError(t, db.Create(&user).Error)
	t.Cleanup(func() { db.Where("user_id = ?", user.Id).Delete(&model.UserSession{}); db.Delete(&user) })
	r := gin.New()
	r.Use(middleware.RequestId())
	SetApiRouter(r)
	return r, user
}

func passwordAuditRequest(r *gin.Engine, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/user/login?cookie=synthetic-query-secret", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "synthetic-cookie-secret")
	req.Header.Set("Authorization", "synthetic-token-secret")
	req.Header.Set("User-Agent", strings.Repeat("synthetic-user-agent-secret", 100))
	req.Header.Set("X-Request-ID", "synthetic-request-id-secret")
	req.RemoteAddr = "192.0.2.41:1000"
	r.ServeHTTP(w, req)
	return w
}

func TestPasswordLoginFailureAuditDatabaseMatrix(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			_, user := setupPasswordFailureAudit(t, kind)
			db, logDB := model.DB, model.LOG_DB
			var credentialFailureBody string
			for _, scenario := range []string{"wrong", "unknown", "disabled", "malformed", "empty", "missing_envelope", "invalid_envelope", "login_disabled", "database_failure", "success"} {
				t.Run(scenario, func(t *testing.T) {
					common.PasswordLoginEnabled, common.PasswordLoginEncryptionEnabled = true, false
					require.NoError(t, db.Model(&user).Update("status", common.UserStatusEnabled).Error)
					input := map[string]any{"username": user.Username, "password": "synthetic-wrong-secret"}
					reason := "invalid_credentials"
					switch scenario {
					case "unknown":
						input["username"] = strings.Repeat("synthetic-unknown-secret\r\n", 1000)
					case "disabled":
						require.NoError(t, db.Model(&user).Update("status", common.UserStatusDisabled).Error)
						input["password"] = "Synthetic-Audit-Only!2026"
					case "empty", "malformed":
						input = map[string]any{}
						reason = "invalid_parameters"
					case "missing_envelope":
						common.PasswordLoginEncryptionEnabled = true
						reason = "invalid_parameters"
					case "invalid_envelope":
						common.PasswordLoginEncryptionEnabled = true
						input["password_encrypted"] = "synthetic-ciphertext-secret"
						input["encryption_key_id"] = "synthetic-invalid-key"
					case "login_disabled":
						common.PasswordLoginEnabled = false
						reason = "login_disabled"
					case "database_failure":
						reason = "system_failure"
						require.NoError(t, db.Callback().Query().Before("gorm:query").Register("synthetic-login-query-error", func(tx *gorm.DB) {
							if tx.Statement.Table == "users" {
								tx.AddError(errors.New("synthetic-database-secret"))
							}
						}))
						t.Cleanup(func() { db.Callback().Query().Remove("synthetic-login-query-error") })
					case "success":
						input["password"] = "Synthetic-Audit-Only!2026"
					}
					body, err := common.Marshal(input)
					require.NoError(t, err)
					if scenario == "malformed" {
						body = []byte("{")
					}
					r := gin.New()
					r.Use(middleware.RequestId())
					SetApiRouter(r)
					w := passwordAuditRequest(r, string(body))
					require.Equal(t, http.StatusOK, w.Code)
					var response struct {
						Success bool `json:"success"`
					}
					require.NoError(t, common.Unmarshal(w.Body.Bytes(), &response))
					require.Equal(t, scenario == "success", response.Success)
					if scenario == "wrong" {
						credentialFailureBody = w.Body.String()
					}
					if scenario == "unknown" || scenario == "disabled" {
						require.Equal(t, credentialFailureBody, w.Body.String(), "do not reveal account existence or disabled state")
					}
					id := w.Header().Get(common.RequestIdKey)
					var rows []model.AuditLog
					require.NoError(t, logDB.Where("request_id = ?", id).Find(&rows).Error)
					t.Cleanup(func() { logDB.Where("request_id = ?", id).Delete(&model.AuditLog{}) })
					require.Len(t, rows, 1, "one real security event, no duplicate operation writer")
					row := rows[0]
					if scenario == "success" {
						require.Equal(t, "login", row.Action)
						require.True(t, row.Success)
						return
					}
					require.Equal(t, "login.failed", row.Action)
					require.False(t, row.Success)
					require.Equal(t, 0, row.UserId)
					require.Zero(t, row.ActorRole)
					require.Equal(t, "anonymous", row.Username)
					require.Empty(t, row.Ip)
					require.Empty(t, row.UserAgent)
					require.Equal(t, "password", row.AuthMethod)
					require.Equal(t, "password", row.Other.LoginMethod)
					require.Positive(t, row.CreatedAt)
					require.Equal(t, id, row.RequestId)
					require.Equal(t, "POST", row.Method)
					require.Equal(t, "/api/user/login", row.Route)
					require.Equal(t, w.Code, row.Status)
					params, err := common.Marshal(row.Other.Op.Params)
					require.NoError(t, err)
					require.Contains(t, string(params), `"reason":"`+reason+`"`)
					encoded, err := common.Marshal(row)
					require.NoError(t, err)
					for _, secret := range []string{user.Username, "192.0.2.41", "synthetic-user-agent-secret", "synthetic-cookie-secret", "synthetic-token-secret", "synthetic-query-secret", "synthetic-request-id-secret", "synthetic-ciphertext-secret", "synthetic-wrong-secret", "synthetic-unknown-secret", "synthetic-database-secret", "Synthetic-Audit-Only!2026"} {
						assert.NotContains(t, string(encoded), secret)
					}
					require.NotContains(t, w.Body.String(), "synthetic-database-secret")
				})
			}
		})
	}
}

func TestPasswordLoginFailureAuditLimits(t *testing.T) {
	for _, scenario := range []string{"critical", "global", "limiter_failure", "audit_budget"} {
		t.Run(scenario, func(t *testing.T) {
			_, _ = setupPasswordFailureAudit(t, "sqlite")
			oldCriticalNum, oldCriticalDuration := common.CriticalRateLimitNum, common.CriticalRateLimitDuration
			oldGlobalNum, oldGlobalDuration := common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration
			t.Cleanup(func() {
				common.CriticalRateLimitNum, common.CriticalRateLimitDuration = oldCriticalNum, oldCriticalDuration
				common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration = oldGlobalNum, oldGlobalDuration
			})
			if scenario == "critical" {
				common.CriticalRateLimitEnable = true
				common.CriticalRateLimitNum, common.CriticalRateLimitDuration = 2, 60
			}
			if scenario == "global" {
				common.GlobalApiRateLimitEnable = true
				common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration = 2, 60
			}
			if scenario == "limiter_failure" {
				oldRedis, oldRDB := common.RedisEnabled, common.RDB
				server := miniredis.RunT(t)
				client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1, DialTimeout: 100 * time.Millisecond})
				server.Close()
				common.RedisEnabled, common.RDB, common.CriticalRateLimitEnable = true, client, true
				t.Cleanup(func() { common.RedisEnabled, common.RDB = oldRedis, oldRDB; client.Close() })
			}
			r := gin.New()
			r.Use(middleware.RequestId())
			SetApiRouter(r)
			n := 3
			if scenario == "audit_budget" {
				n = 61
			}
			for i := range n {
				w := passwordAuditRequest(r, "{}")
				want := 200
				if scenario == "limiter_failure" {
					want = 500
				} else if scenario != "audit_budget" && i == 2 {
					want = 429
					require.NotEmpty(t, w.Header().Get("Retry-After"))
				}
				require.Equal(t, want, w.Code)
			}
			var rows []model.AuditLog
			require.NoError(t, model.LOG_DB.Where("action = ?", "login.failed").Find(&rows).Error)
			want := 3
			if scenario == "audit_budget" {
				want = 60
			}
			require.Len(t, rows, want)
			if scenario == "critical" || scenario == "global" {
				var count int
				for _, row := range rows {
					data, err := common.Marshal(row.Other)
					require.NoError(t, err)
					if strings.Contains(string(data), `"reason":"rate_limited"`) {
						count++
					}
				}
				require.Equal(t, 1, count)
			}
		})
	}
}

func TestPasswordLoginFailureAuditStorageFailure(t *testing.T) {
	for _, scenario := range []string{"unavailable", "write_error", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			r, _ := setupPasswordFailureAudit(t, "sqlite")
			db := model.LOG_DB
			if scenario == "unavailable" {
				model.LOG_DB = nil
			} else {
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("synthetic-login-audit-error", func(tx *gorm.DB) {
					if tx.Statement.Table != "audit_logs" {
						return
					}
					if scenario == "deadline" {
						_, exists := tx.Statement.Context.Deadline()
						assert.True(t, exists)
						<-tx.Statement.Context.Done()
						tx.AddError(tx.Statement.Context.Err())
					} else {
						tx.AddError(errors.New("synthetic-sensitive-audit-error"))
					}
				}))
			}
			w := passwordAuditRequest(r, "{}")
			require.Equal(t, 200, w.Code)
			require.Contains(t, w.Body.String(), `"success":false`)
			require.NotContains(t, w.Body.String(), "synthetic-sensitive-audit-error")
			model.LOG_DB = db
			if scenario != "unavailable" {
				require.NoError(t, db.Callback().Create().Remove("synthetic-login-audit-error"))
			}
			w = passwordAuditRequest(r, "{}")
			var row model.AuditLog
			require.NoError(t, db.Where("request_id = ?", w.Header().Get(common.RequestIdKey)).First(&row).Error)
			data, err := common.Marshal(row.Other)
			require.NoError(t, err)
			require.Contains(t, string(data), `"failed_events":1`)
		})
	}
}

func TestPasswordLoginFailureAuditConcurrentBound(t *testing.T) {
	r, _ := setupPasswordFailureAudit(t, "sqlite")
	db := model.LOG_DB
	started, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	require.NoError(t, db.Callback().Create().Before("gorm:begin_transaction").Register("synthetic-login-audit-block", func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_logs" {
			started <- struct{}{}
			<-release
		}
	}))
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wg.Go(func() { statuses <- passwordAuditRequest(r, "{}").Code })
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("audit writer barrier unavailable")
		}
	}
	w := passwordAuditRequest(r, "{}")
	require.Equal(t, 200, w.Code)
	once.Do(func() { close(release) })
	wg.Wait()
	close(statuses)
	for status := range statuses {
		require.Equal(t, 200, status)
	}
	require.NoError(t, db.Callback().Create().Remove("synthetic-login-audit-block"))
	w = passwordAuditRequest(r, "{}")
	var row model.AuditLog
	require.NoError(t, db.Where("request_id = ?", w.Header().Get(common.RequestIdKey)).First(&row).Error)
	data, err := common.Marshal(row.Other)
	require.NoError(t, err)
	require.Contains(t, string(data), `"suppressed_events":1`)
	var count int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "login.failed").Count(&count).Error)
	require.EqualValues(t, 3, count)
}
