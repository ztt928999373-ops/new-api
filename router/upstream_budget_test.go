package router

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/service/credentials"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupUpstreamBudgetDB(t *testing.T, kind string) *gorm.DB {
	t.Helper()
	require.NoError(t, i18n.Init())
	setupRelayRouterTestDB(t) // initialize host column quoting without real state
	var dialect gorm.Dialector = sqlite.Open(filepath.Join(t.TempDir(), "budget.sqlite"))
	typeID := common.DatabaseTypeSQLite
	if kind != "sqlite" {
		if os.Getenv("SUANJIE_TEST_DATABASES") != "local-synthetic" {
			t.Skip("isolated synthetic database not configured")
		}
		if kind == "mysql" {
			typeID = common.DatabaseTypeMySQL
			dialect = mysql.Open("root:synthetic-test-only@tcp(127.0.0.1:23316)/credential_test?parseTime=true")
		} else {
			typeID = common.DatabaseTypePostgreSQL
			dialect = postgres.Open("host=127.0.0.1 port=25436 user=postgres password=synthetic-test-only dbname=credential_test sslmode=disable")
		}
	}
	db, err := gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	conn, err := db.DB()
	require.NoError(t, err)
	conn.SetMaxOpenConns(4)
	if kind == "sqlite" {
		conn.SetMaxOpenConns(1)
		require.NoError(t, db.Exec("PRAGMA busy_timeout=5000").Error)
	}
	var version string
	versionSQL := "SELECT VERSION()"
	if kind == "sqlite" {
		versionSQL = "SELECT sqlite_version()"
	}
	require.NoError(t, db.Raw(versionSQL).Scan(&version).Error)
	t.Logf("budget database=%s engine=%s", kind, version)
	oldDB, oldLog := model.DB, model.LOG_DB
	oldMain, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(typeID, typeID)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLog
		common.SetDatabaseTypes(oldMain, oldLogType)
		require.NoError(t, conn.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	return db
}

func installBudgetTestCredential(t *testing.T, db *gorm.DB, userID int) string {
	t.Helper()
	keyDir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, credentialcrypto.InitializeLocalDirectory(keyDir))
	keyPath := filepath.Join(keyDir, "ring")
	require.NoError(t, credentialcrypto.InitializeLocalFile(keyPath))
	provider, err := credentialcrypto.NewLocalFileKeyProvider(keyPath)
	require.NoError(t, err)
	store := &credentials.Store{DB: db, Environment: "test", ProviderID: "local-test", Provider: provider}
	require.NoError(t, store.MigrateSchema())
	head, err := store.Create(context.Background(), credentials.Principal{TenantID: "default", UserID: fmt.Sprint(userID), PlatformOperator: true}, true, "openai", "api_key", "relay", []byte("synthetic-budget-supplier"))
	require.NoError(t, err)
	previous := service.SetChannelCredentialResolver(&credentials.Resolver{Environment: "test", Repository: store, Providers: map[string]credentialcrypto.KeyProvider{"local-test": provider}, Audit: store})
	t.Cleanup(func() { service.SetChannelCredentialResolver(previous) })
	return head.ID
}

func TestUpstreamBudgetAuthenticatedRetryStopsBeforeSecondSend(t *testing.T) {
	db := setupUpstreamBudgetDB(t, "sqlite")
	require.NoError(t, db.AutoMigrate(&model.UpstreamBudget{}, &model.UpstreamCostReservation{}, &model.User{}, &model.Token{}, &model.Ability{}, &model.Channel{}, &model.Option{}, &model.Log{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}))
	require.NoError(t, db.AutoMigrate(&model.UserSession{}, &model.UserAccessToken{}, &model.AuditLog{}, &model.CasbinRule{}, &model.AuthzRole{}))
	require.NoError(t, authz.Init(db))
	master, sessionSecret := common.IsMasterNode, common.SessionSecret
	common.IsMasterNode, common.SessionSecret = true, "synthetic-budget-freeze-session"
	t.Cleanup(func() { common.IsMasterNode, common.SessionSecret = master, sessionSecret })
	root := model.User{Username: "synthetic-budget-root", Role: 100, Status: 1, Group: "default", AffCode: "budget-root"}
	require.NoError(t, db.Create(&root).Error)
	login, err := service.CreateLoginSession(root.Id, "synthetic", "127.0.0.1", "synthetic")
	require.NoError(t, err)
	ch, p := syntheticBudgetChannel(t, db)
	p.LimitMicros = 10
	require.NoError(t, model.ConfigureUpstreamBudget(context.Background(), db, p, 1))
	p.Revision = 2
	user := model.User{Username: "synthetic-budget-client", Role: 1, Status: 1, Group: "default", Quota: 1000000, AffCode: "budget-client"}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "syntheticbudgetclient", Status: 1, ExpiredTime: -1, RemainQuota: 1000000}
	require.NoError(t, db.Create(&token).Error)
	credentialID := installBudgetTestCredential(t, db, user.Id)
	require.NoError(t, db.Model(&ch).Updates(map[string]any{"credential_ref": credentialID, "models": "synthetic-model", "group": "default"}).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: ch.Id, Group: "default", Model: "synthetic-model", Enabled: true}).Error)
	previousPrices := ratio_setting.ModelPrice2JSONString()
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"synthetic-model":0.002}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrices)) })
	oldRetries, oldMemory, oldBatch := common.RetryTimes, common.MemoryCacheEnabled, common.BatchUpdateEnabled
	common.RetryTimes, common.MemoryCacheEnabled, common.BatchUpdateEnabled = 2, false, false
	t.Cleanup(func() {
		common.RetryTimes, common.MemoryCacheEnabled, common.BatchUpdateEnabled = oldRetries, oldMemory, oldBatch
	})
	service.InitHttpClient()
	client := service.GetHttpClient()
	original := client.Transport
	t.Cleanup(func() { client.Transport = original })
	var calls atomic.Int64
	client.Transport = pilotTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		require.Equal(t, "Bearer synthetic-budget-supplier", req.Header.Get("Authorization"))
		return &http.Response{StatusCode: 500, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"synthetic failure"}}`))}, nil
	})
	// Refunds run in the existing worker pool. Drain even when an assertion
	// fails, before cleanup restores database and billing globals.
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond)
	})
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(common.RequestIdKey, "synthetic-budget-freeze"); c.Next() })
	registerChannelRoutes(engine.Group("/api"))
	SetRelayRouter(engine)
	for index := range 2 {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"synthetic-model","messages":[{"role":"user","content":"hi"}],"max_tokens":16}`))
		req.Header.Set("Authorization", "Bearer syntheticbudgetclient")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		require.GreaterOrEqual(t, w.Code, 400, w.Body.String())
		require.NotContains(t, w.Body.String(), "synthetic-budget-supplier")
		if index == 0 {
			// Close the budget through the real authorized management route while
			// the channel remains enabled, then repeat the ordinary relay call.
			p.Enabled = false
			data, err := common.Marshal(p)
			require.NoError(t, err)
			var input map[string]any
			require.NoError(t, common.Unmarshal(data, &input))
			input["expected_revision"] = p.Revision
			data, err = common.Marshal(input)
			require.NoError(t, err)
			request := httptest.NewRequest("PUT", fmt.Sprintf("/api/channel/%d/upstream-budget", ch.Id), strings.NewReader(string(data)))
			request.Header.Set("Authorization", "Bearer "+login.AccessToken)
			request.Header.Set("Content-Type", "application/json")
			result := httptest.NewRecorder()
			engine.ServeHTTP(result, request)
			require.Equal(t, 200, result.Code, result.Body.String())
			require.Contains(t, result.Body.String(), `"protection":"retained"`)
			var frozen model.Channel
			require.NoError(t, db.First(&frozen, ch.Id).Error)
			require.Equal(t, common.ChannelStatusEnabled, frozen.Status)
			require.True(t, frozen.UpstreamBudgetEnabled)
			var audit model.AuditLog
			require.NoError(t, db.Where("action = ?", "upstream_budget.configure").First(&audit).Error)
			require.True(t, audit.Success)
			require.Equal(t, root.Id, audit.UserId)
			require.Equal(t, "synthetic-budget-freeze", audit.RequestId)
			encoded, err := common.Marshal(audit)
			require.NoError(t, err)
			require.Contains(t, string(encoded), "retained")
			require.NotContains(t, string(encoded), login.AccessToken)
			require.NotContains(t, string(encoded), "synthetic-budget-supplier")
		}
	}
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond)
	require.EqualValues(t, 1, calls.Load(), "budget denial skips retry and never submits a second payable attempt")
	var after model.UpstreamBudget
	require.NoError(t, db.First(&after, "channel_id = ?", ch.Id).Error)
	require.EqualValues(t, 10, after.ChargedMicros)
	require.Zero(t, after.HeldMicros)
	var wallet model.User
	require.NoError(t, db.First(&wallet, user.Id).Error)
	require.EqualValues(t, 1000000, wallet.Quota, "wallet refund does not release upstream liability")
}

func syntheticBudgetChannel(t *testing.T, db *gorm.DB) (model.Channel, model.UpstreamBudget) {
	t.Helper()
	ch := model.Channel{Type: constant.ChannelTypeOpenAI, CredentialRef: "synthetic-reference", Name: "synthetic-budget", Status: common.ChannelStatusManuallyDisabled}
	require.NoError(t, db.Create(&ch).Error)
	t.Cleanup(func() {
		if db.Migrator().HasTable(&model.SupplierCostStatement{}) {
			db.Where("channel_id = ?", ch.Id).Delete(&model.SupplierCostStatement{})
		}
		db.Where("channel_id = ?", ch.Id).Delete(&model.UpstreamCostReservation{})
		db.Where("channel_id = ?", ch.Id).Delete(&model.UpstreamBudget{})
		db.Delete(&ch)
	})
	p := model.UpstreamBudget{ChannelID: ch.Id, Enabled: true, LimitMicros: 100, AttemptCeilingMicros: 10, Model: "synthetic-model", MaxRequestBytes: 4096, MaxOutputTokens: 64, ValidUntil: time.Now().Unix() + 3600, CeilingConfirmed: true, CostBasisHash: strings.Repeat("a", 64)}
	require.NoError(t, model.ConfigureUpstreamBudget(context.Background(), db, p, 0))
	require.NoError(t, db.Model(&ch).Update("status", common.ChannelStatusEnabled).Error)
	p.Revision = 1
	return ch, p
}

func TestUpstreamBudgetDatabaseMatrix(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			db := setupUpstreamBudgetDB(t, kind)
			// Reconstruct the prior channel schema, then perform the additive upgrade twice.
			legacy := model.Channel{Type: 1, Name: "legacy-preserved", Key: "synthetic-old-key"}
			require.NoError(t, db.Create(&legacy).Error)
			t.Cleanup(func() { db.Delete(&legacy) })
			require.NoError(t, db.Migrator().DropColumn(&model.Channel{}, "upstream_budget_enabled"))
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.UpstreamBudget{}, &model.UpstreamCostReservation{}))
			}
			var old model.Channel
			require.NoError(t, db.First(&old, legacy.Id).Error)
			require.False(t, old.UpstreamBudgetEnabled)
			require.Equal(t, legacy.Key, old.Key)
			ch, p := syntheticBudgetChannel(t, db)
			ctx := context.Background()
			results := make(chan model.UpstreamCostReservation, 20)
			var wg sync.WaitGroup
			for range 20 {
				wg.Go(func() {
					r, err := model.ReserveUpstreamCost(ctx, db, p)
					if err == nil {
						results <- r
					}
				})
			}
			wg.Wait()
			close(results)
			var reserved []model.UpstreamCostReservation
			for r := range results {
				reserved = append(reserved, r)
			}
			require.Len(t, reserved, 10, "no concurrent oversubscription")
			var state model.UpstreamBudget
			require.NoError(t, db.First(&state, "channel_id = ?", ch.Id).Error)
			require.EqualValues(t, 100, state.HeldMicros)
			p.LimitMicros = 99
			require.Error(t, model.ConfigureUpstreamBudget(ctx, db, p, 1))
			p.LimitMicros = 100
			require.Error(t, model.ConfigureUpstreamBudget(ctx, db, p, 0))
			require.NoError(t, model.ChargeUpstreamCeiling(ctx, db, reserved[0].ID))
			require.NoError(t, model.ChargeUpstreamCeiling(ctx, db, reserved[0].ID))
			require.NoError(t, model.ReconcileUpstreamCost(ctx, db, reserved[0].ID, 4, "synthetic-invoice-one"))
			require.NoError(t, model.ReconcileUpstreamCost(ctx, db, reserved[0].ID, 4, "synthetic-invoice-one"))
			require.Error(t, model.ReconcileUpstreamCost(ctx, db, reserved[0].ID, 5, "synthetic-invoice-one"))
			require.NoError(t, db.First(&state, "channel_id = ?", ch.Id).Error)
			require.EqualValues(t, 90, state.HeldMicros)
			require.EqualValues(t, 4, state.ChargedMicros)
			_, err := model.ReserveUpstreamCost(ctx, db, p)
			require.Error(t, err)
			// An independent connection sees held liabilities; reopening never resets them.
			reopened, err := gorm.Open(db.Dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			connection, err := reopened.DB()
			require.NoError(t, err)
			defer connection.Close()
			var afterRestart model.UpstreamBudget
			require.NoError(t, reopened.First(&afterRestart, "channel_id = ?", ch.Id).Error)
			require.Equal(t, state.HeldMicros, afterRestart.HeldMicros)
			require.NoError(t, model.ReconcileUpstreamCost(ctx, db, reserved[1].ID, 0, "synthetic-zero-cost-proof"))
			// Failure between atomic reserve and ledger insert must roll everything back.
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("synthetic-budget-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "upstream_cost_reservations" {
					tx.AddError(errors.New("synthetic-ledger-failure"))
				}
			}))
			_, err = model.ReserveUpstreamCost(ctx, db, p)
			require.Error(t, err)
			require.NoError(t, db.Callback().Create().Remove("synthetic-budget-fail"))
			require.NoError(t, db.First(&state, "channel_id = ?", ch.Id).Error)
			require.EqualValues(t, 80, state.HeldMicros)
			bad := p
			bad.AttemptCeilingMicros = 1
			_, err = model.ReserveUpstreamCost(ctx, db, bad)
			require.Error(t, err)
			bad = p
			bad.LimitMicros = model.MaxUpstreamBudgetMicros + 1
			require.Error(t, model.ConfigureUpstreamBudget(ctx, db, bad, 1))
			bad = p
			bad.AttemptCeilingMicros = -1
			require.Error(t, model.ConfigureUpstreamBudget(ctx, db, bad, 1))
			bad = p
			bad.CeilingConfirmed = false
			require.Error(t, model.ConfigureUpstreamBudget(ctx, db, bad, 1))
			// A verified invoice above the declared ceiling is NOT clipped: freeze.
			require.NoError(t, model.ReconcileUpstreamCost(ctx, db, reserved[2].ID, 20, "synthetic-breached-declaration"))
			require.NoError(t, db.First(&state, "channel_id = ?", ch.Id).Error)
			require.False(t, state.Enabled)
			_, err = model.ReserveUpstreamCost(ctx, db, p)
			require.Error(t, err)
			var marker model.Channel
			require.NoError(t, db.First(&marker, ch.Id).Error)
			require.True(t, marker.UpstreamBudgetEnabled, "breach freezes admission, never opts out")
			// Closing a policy with outstanding liabilities keeps both counters
			// and the protected marker. This also models reconfiguring a restore freeze.
			p.Enabled = false
			require.NoError(t, model.ConfigureUpstreamBudget(ctx, db, p, 1))
			var frozen model.UpstreamBudget
			require.NoError(t, db.First(&frozen, "channel_id = ?", ch.Id).Error)
			require.False(t, frozen.Enabled)
			require.Equal(t, state.HeldMicros, frozen.HeldMicros)
			require.Equal(t, state.ChargedMicros, frozen.ChargedMicros)
			require.NoError(t, db.First(&marker, ch.Id).Error)
			require.True(t, marker.UpstreamBudgetEnabled)
			p.Revision = frozen.Revision
			_, err = model.ReserveUpstreamCost(ctx, db, p)
			require.Error(t, err)
			// Multiple above-ceiling invoices can exceed the configured budget.
			// Even then an outstanding attempt must never wrap its final charge.
			require.NoError(t, db.Model(&state).Update("charged_micros", int64(math.MaxInt64)).Error)
			require.Error(t, model.ChargeUpstreamCeiling(ctx, db, reserved[3].ID))
			var outstanding model.UpstreamCostReservation
			require.NoError(t, db.First(&outstanding, "id = ?", reserved[3].ID).Error)
			require.Equal(t, "held", outstanding.State, "overflow rolls back the ledger transition")
			require.NoError(t, db.First(&state, "channel_id = ?", ch.Id).Error)
			require.EqualValues(t, math.MaxInt64, state.ChargedMicros)
			require.EqualValues(t, 70, state.HeldMicros)
		})
	}
}

func TestUpstreamBudgetHTTPAttemptsAndPermissions(t *testing.T) {
	db := setupUpstreamBudgetDB(t, "sqlite")
	require.NoError(t, db.AutoMigrate(&model.UpstreamBudget{}, &model.UpstreamCostReservation{}, &model.SupplierCostStatement{}, &model.Log{}, &model.User{}, &model.UserSession{}, &model.UserAccessToken{}, &model.Option{}, &model.AuditLog{}, &model.CasbinRule{}, &model.AuthzRole{}))
	previousMaster, previousSecret := common.IsMasterNode, common.SessionSecret
	common.IsMasterNode = true
	common.SessionSecret = "synthetic-budget-session-secret"
	t.Cleanup(func() { common.IsMasterNode, common.SessionSecret = previousMaster, previousSecret })
	require.NoError(t, authz.Init(db))
	ch, p := syntheticBudgetChannel(t, db)
	tokens := map[int]string{}
	for _, role := range []int{1, 10, 100} {
		u := model.User{Username: fmt.Sprintf("budget-role-%d", role), Role: role, Status: 1, AuthVersion: 1, Quota: 123, AffCode: fmt.Sprint(role)}
		require.NoError(t, db.Create(&u).Error)
		bundle, err := service.CreateLoginSession(u.Id, "synthetic", "127.0.0.1", "synthetic")
		require.NoError(t, err)
		tokens[role] = bundle.AccessToken
	}
	api := gin.New()
	registerChannelRoutes(api.Group("/api"))
	for _, role := range []int{0, 1, 10, 100} {
		for _, operation := range []struct{ method, suffix string }{{"GET", ""}, {"PUT", ""}, {"POST", "/reconcile"}, {"GET", "/statements"}, {"POST", "/statements"}} {
			method := operation.method
			path := fmt.Sprintf("/api/channel/%d/upstream-budget%s", ch.Id, operation.suffix)
			req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			if role > 0 {
				req.Header.Set("Authorization", "Bearer "+tokens[role])
			}
			w := httptest.NewRecorder()
			api.ServeHTTP(w, req)
			if role == 0 {
				require.Equal(t, 401, w.Code)
			} else if role < 100 {
				require.Equal(t, 403, w.Code)
			} else if method == "GET" {
				require.Equal(t, 200, w.Code)
			} else {
				require.NotEqual(t, 401, w.Code)
				require.NotEqual(t, 403, w.Code)
			}
		}
	}
	data, err := common.Marshal(p)
	require.NoError(t, err)
	var configuration map[string]any
	require.NoError(t, common.Unmarshal(data, &configuration))
	configuration["expected_revision"] = 1
	configuration["cost_basis"] = "synthetic-reviewed-upper-cost-contract"
	configuration["held_micros"] = 999
	configuration["allocated_liability_micros"] = 999
	data, err = common.Marshal(configuration)
	require.NoError(t, err)
	configureReq := httptest.NewRequest("PUT", fmt.Sprintf("/api/channel/%d/upstream-budget", ch.Id), strings.NewReader(string(data)))
	configureReq.Header.Set("Content-Type", "application/json")
	configureReq.Header.Set("Authorization", "Bearer "+tokens[100])
	configured := httptest.NewRecorder()
	api.ServeHTTP(configured, configureReq)
	require.Equal(t, 200, configured.Code, configured.Body.String())
	var configuredState model.UpstreamBudget
	require.NoError(t, db.First(&configuredState, "channel_id = ?", ch.Id).Error)
	require.Zero(t, configuredState.HeldMicros)
	require.Zero(t, configuredState.ChargedMicros, "client cannot overwrite financial counters")
	p.Revision = 2
	service.InitHttpClient()
	client := service.GetHttpClient()
	original := client.Transport
	t.Cleanup(func() { client.Transport = original })
	var calls atomic.Int64
	fail := false
	client.Transport = pilotTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if fail {
			return nil, errors.New("synthetic-timeout")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`data: synthetic\n\n`))}, nil
	})
	const body = `{"model":"synthetic-model","messages":[{"role":"user","content":"synthetic-prompt"}],"max_tokens":16,"stream":true}`
	send := func(payload string) (*http.Response, error) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(payload))
		c.Set(service.UpstreamBudgetManagedKey, true)
		out, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", strings.NewReader(payload))
		require.NoError(t, err)
		return channel.DoRequest(c, out, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: ch.Id, ChannelType: constant.ChannelTypeOpenAI}})
	}
	for _, invalid := range []string{`{}`, strings.Replace(body, "synthetic-model", "unknown-model", 1), strings.Replace(body, `"max_tokens":16`, `"max_tokens":999`, 1), strings.Replace(body, `"stream":true`, `"tools":[]`, 1), strings.Replace(body, `"content":"synthetic-prompt"`, `"content":[{"type":"image_url"}]`, 1)} {
		_, err := send(invalid)
		require.Error(t, err)
		require.Zero(t, calls.Load())
	}
	resp, err := send(body)
	require.NoError(t, err)
	var state model.UpstreamBudget
	require.NoError(t, db.First(&state, "channel_id = ?", ch.Id).Error)
	require.EqualValues(t, 10, state.HeldMicros)
	// In-flight stream is reserved, even before the response is read or closed.
	fail = true
	_, err = send(body)
	require.Error(t, err)
	fail = false
	for range 8 {
		r, err := send(body)
		require.NoError(t, err)
		require.NoError(t, r.Body.Close())
	}
	_, err = send(body)
	require.Error(t, err)
	require.EqualValues(t, 10, calls.Load(), "over-budget attempt never reaches transport")
	require.NoError(t, resp.Body.Close())
	require.NoError(t, resp.Body.Close())
	state = model.UpstreamBudget{}
	require.NoError(t, db.First(&state, "channel_id = ?", ch.Id).Error)
	require.Zero(t, state.HeldMicros)
	require.EqualValues(t, 100, state.ChargedMicros)
	// User quota is independent; no automatic release on any wallet refund.
	var users []model.User
	require.NoError(t, db.Find(&users).Error)
	for _, u := range users {
		require.EqualValues(t, 123, u.Quota)
	}
	// Expiry rejects before dispatch and does not free old liabilities.
	require.NoError(t, db.Model(&model.UpstreamBudget{}).Where("channel_id = ?", ch.Id).Update("valid_until", time.Now().Unix()-1).Error)
	_, err = send(body)
	require.Error(t, err)
	require.EqualValues(t, 10, calls.Load())
	// Disabling a configured budget freezes dispatch; it never opts out.
	p.Enabled = false
	p.ValidUntil = time.Now().Unix() + 3600
	require.NoError(t, model.ConfigureUpstreamBudget(context.Background(), db, p, p.Revision))
	_, err = send(body)
	require.Error(t, err)
	require.EqualValues(t, 10, calls.Load(), "disabled budget must not dispatch an unreserved request")
	var frozen model.Channel
	require.NoError(t, db.First(&frozen, ch.Id).Error)
	require.True(t, frozen.UpstreamBudgetEnabled)
	state = model.UpstreamBudget{}
	require.NoError(t, db.First(&state, "channel_id = ?", ch.Id).Error)
	require.EqualValues(t, 100, state.ChargedMicros)
	// A final operator-attested bill differs from both wallet quota and the
	// reserved ceiling. It creates a persistent alert, never invented revenue.
	var paidAttempt model.UpstreamCostReservation
	require.NoError(t, db.Where("channel_id = ?", ch.Id).First(&paidAttempt).Error)
	estimate := int64(8)
	bill := model.SupplierCostInput{Account: "synthetic-private-account", Bill: "synthetic-private-invoice", Line: "one", Currency: "USD", ReservationID: paidAttempt.ID, ActualMicros: 11, EstimatedMicros: &estimate, Evidence: "synthetic-private-bill-proof", Confirmed: true}
	data, err = common.Marshal(bill)
	require.NoError(t, err)
	var missingAmount map[string]any
	require.NoError(t, common.Unmarshal(data, &missingAmount))
	delete(missingAmount, "actual_micros")
	missingData, err := common.Marshal(missingAmount)
	require.NoError(t, err)
	missingRequest := httptest.NewRequest("POST", fmt.Sprintf("/api/channel/%d/upstream-budget/statements", ch.Id), strings.NewReader(string(missingData)))
	missingRequest.Header.Set("Authorization", "Bearer "+tokens[100])
	missingRequest.Header.Set("Content-Type", "application/json")
	missingResponse := httptest.NewRecorder()
	api.ServeHTTP(missingResponse, missingRequest)
	require.Equal(t, 400, missingResponse.Code, "missing actual cost is not a free bill")
	importRequest := httptest.NewRequest("POST", fmt.Sprintf("/api/channel/%d/upstream-budget/statements", ch.Id), strings.NewReader(string(data)))
	importRequest.Header.Set("Authorization", "Bearer "+tokens[100])
	importRequest.Header.Set("Content-Type", "application/json")
	imported := httptest.NewRecorder()
	api.ServeHTTP(imported, importRequest)
	require.Equal(t, 200, imported.Code, imported.Body.String())
	require.Contains(t, imported.Body.String(), "above_reserved_ceiling")
	require.NoError(t, db.Create(&model.Log{RequestId: paidAttempt.RequestID, ChannelId: ch.Id, Type: model.LogTypeConsume, Quota: 123}).Error)
	view := httptest.NewRequest("GET", fmt.Sprintf("/api/channel/%d/upstream-budget/statements", ch.Id), nil)
	view.Header.Set("Authorization", "Bearer "+tokens[100])
	viewed := httptest.NewRecorder()
	api.ServeHTTP(viewed, view)
	require.Equal(t, 200, viewed.Code, viewed.Body.String())
	var financial map[string]any
	require.NoError(t, common.Unmarshal(viewed.Body.Bytes(), &financial))
	require.Nil(t, financial["platform_revenue_usd_micros"])
	require.Nil(t, financial["gross_profit_usd_micros"])
	require.Equal(t, "quota", financial["user_charge_unit"])
	require.Len(t, financial["user_charge_references"], 1)
	var audit model.AuditLog
	require.NoError(t, db.Where("action = ?", "upstream_budget.bill_review").First(&audit).Error)
	auditData, err := common.Marshal(audit)
	require.NoError(t, err)
	for _, sensitive := range []string{bill.Account, bill.Bill, bill.Evidence, tokens[100]} {
		require.NotContains(t, imported.Body.String()+viewed.Body.String()+string(auditData), sensitive)
	}
	bill.ActualMicros = 12
	conflictingData, err := common.Marshal(bill)
	require.NoError(t, err)
	conflictRequest := httptest.NewRequest("POST", fmt.Sprintf("/api/channel/%d/upstream-budget/statements", ch.Id), strings.NewReader(string(conflictingData)))
	conflictRequest.Header.Set("Authorization", "Bearer "+tokens[100])
	conflictRequest.Header.Set("Content-Type", "application/json")
	conflicted := httptest.NewRecorder()
	api.ServeHTTP(conflicted, conflictRequest)
	require.Equal(t, 409, conflicted.Code)
	var conflictAudit model.AuditLog
	require.NoError(t, db.Where("action = ?", "upstream_budget.bill_review").Order("id DESC").First(&conflictAudit).Error)
	require.Equal(t, 409, conflictAudit.Status)
	require.False(t, conflictAudit.Success, "a rejected bill cannot be audited as a successful operation")
	// Even a forced enabled bit cannot bypass the independent frozen gate.
	require.NoError(t, db.Model(&model.UpstreamBudget{}).Where("channel_id = ?", ch.Id).Update("enabled", true).Error)
	_, err = send(body)
	require.Error(t, err)
	require.EqualValues(t, 10, calls.Load())
}

func TestUpstreamBudgetWebSocketRoutes(t *testing.T) {
	db := setupUpstreamBudgetDB(t, "sqlite")
	require.NoError(t, db.AutoMigrate(&model.UpstreamBudget{}, &model.UpstreamCostReservation{}, &model.User{}, &model.Token{}, &model.Ability{}, &model.Option{}, &model.Log{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}))
	ch, _ := syntheticBudgetChannel(t, db)
	user := model.User{Username: "synthetic-budget-ws", Role: 1, Status: 1, Group: "default", Quota: 1000000, AffCode: "budget-ws"}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "syntheticbudgetws", Status: 1, ExpiredTime: -1, RemainQuota: 1000000}
	require.NoError(t, db.Create(&token).Error)
	credentialID := installBudgetTestCredential(t, db, user.Id)
	require.NoError(t, db.Model(&ch).Updates(map[string]any{"models": "synthetic-model", "group": "default"}).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: ch.Id, Group: "default", Model: "synthetic-model", Enabled: true}).Error)
	prices := ratio_setting.ModelPrice2JSONString()
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"synthetic-model":0.002}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(prices)) })
	retries, memory, batch := common.RetryTimes, common.MemoryCacheEnabled, common.BatchUpdateEnabled
	common.RetryTimes, common.MemoryCacheEnabled, common.BatchUpdateEnabled = 0, false, false
	t.Cleanup(func() {
		common.RetryTimes, common.MemoryCacheEnabled, common.BatchUpdateEnabled = retries, memory, batch
	})
	// Intercept the real upstream WebSocket dial, never resolve or connect to a supplier.
	previousDialer := websocket.DefaultDialer
	dialer := *previousDialer
	var upstreamDials atomic.Int64
	dialer.NetDialContext = func(context.Context, string, string) (net.Conn, error) {
		upstreamDials.Add(1)
		return nil, errors.New("synthetic-upstream-dial-intercepted")
	}
	websocket.DefaultDialer = &dialer
	t.Cleanup(func() { websocket.DefaultDialer = previousDialer })
	finished := make(chan struct{}, 1)
	engine := gin.New()
	engine.Use(func(c *gin.Context) { defer func() { finished <- struct{}{} }(); c.Next() })
	SetRelayRouter(engine)
	gateway := httptest.NewServer(engine)
	t.Cleanup(gateway.Close)
	// This also proves the instrumented route can reach DoWssRequest when no
	// protection is configured. A detached credential binding must not remove
	// an existing budget's final egress guard.
	for _, scenario := range []struct {
		name                        string
		managed, protected, enabled bool
	}{
		{"managed", true, true, true},
		{"protected_binding_detached", false, true, true},
		{"frozen_binding_detached", false, true, false},
		{"cost_review_frozen_binding_detached", false, true, true},
		{"unconfigured_legacy_control", false, false, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ref := ""
			settings, err := common.Marshal(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
			require.NoError(t, err)
			key := "synthetic-ws-supplier"
			if scenario.managed {
				ref, key, settings = credentialID, "", nil
			}
			require.NoError(t, db.Model(&ch).Updates(map[string]any{"credential_ref": ref, "key": key, "setting": string(settings), "upstream_budget_enabled": scenario.protected}).Error)
			require.NoError(t, db.Model(&model.UpstreamBudget{}).Where("channel_id = ?", ch.Id).Updates(map[string]any{"enabled": scenario.enabled, "reconciliation_frozen": scenario.name == "cost_review_frozen_binding_detached"}).Error)
			for _, protocol := range []string{"realtime?model=synthetic-model", "responses"} {
				t.Run(protocol, func(t *testing.T) {
					before := upstreamDials.Load()
					clientDialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
					client, response, err := clientDialer.Dial("ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/"+protocol, http.Header{"Authorization": {"Bearer syntheticbudgetws"}})
					t.Cleanup(func() {
						if client != nil {
							_ = client.Close()
						}
						select {
						case <-finished:
						case <-time.After(5 * time.Second):
							t.Error("WebSocket route did not finish")
						}
						require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond)
					})
					if err == nil {
						require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
						if protocol == "responses" {
							require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","response":{"model":"synthetic-model","input":"hi","max_output_tokens":16}}`)))
						}
						_, payload, readErr := client.ReadMessage()
						require.NoError(t, readErr)
						require.Contains(t, string(payload), `"error"`)
						require.NotContains(t, string(payload), "synthetic-ws-supplier")
					} else {
						require.NotNil(t, response)
						require.GreaterOrEqual(t, response.StatusCode, 400)
						_ = response.Body.Close()
					}
					if scenario.protected {
						require.Equal(t, before, upstreamDials.Load(), "protected protocol must never dial upstream without reservation")
					} else {
						require.Equal(t, before+1, upstreamDials.Load(), "positive control must reach the intercepted upstream dial")
					}
					var count int64
					require.NoError(t, db.Model(&model.UpstreamCostReservation{}).Count(&count).Error)
					require.Zero(t, count)
				})
			}
		})
	}
}

func TestUpstreamBudgetCancellationAndUnsupportedProtocols(t *testing.T) {
	db := setupUpstreamBudgetDB(t, "sqlite")
	require.NoError(t, db.AutoMigrate(&model.UpstreamBudget{}, &model.UpstreamCostReservation{}))
	ch, _ := syntheticBudgetChannel(t, db)
	for _, path := range []string{"/v1/tasks/synthetic", "/v1/responses", "/v1/realtime", "/v1/images/generations"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		c.Set(service.UpstreamBudgetManagedKey, true)
		out, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", strings.NewReader(`{}`))
		require.NoError(t, err)
		_, err = service.BeginUpstreamBudget(c, out, ch.Id, constant.ChannelTypeOpenAI)
		require.Error(t, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
	c.Set(service.UpstreamBudgetManagedKey, true)
	out, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", strings.NewReader(`{}`))
	require.NoError(t, err)
	_, err = service.BeginUpstreamBudget(c, out, ch.Id, constant.ChannelTypeOpenAI)
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&model.UpstreamCostReservation{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestSupplierCostDatabaseMatrix(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			db := setupUpstreamBudgetDB(t, kind)
			require.NoError(t, db.AutoMigrate(&model.UpstreamBudget{}, &model.UpstreamCostReservation{}, &model.SupplierCostStatement{}))
			ch, policy := syntheticBudgetChannel(t, db)
			// Representative F2 schema: the new gate did not exist. Repeated
			// startup preserves the old policy and nullable false admission.
			require.NoError(t, db.Migrator().DropColumn(&model.UpstreamBudget{}, "reconciliation_frozen"))
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.UpstreamBudget{}, &model.UpstreamCostReservation{}, &model.SupplierCostStatement{}))
			}
			ctx := context.Background()
			r, err := model.ReserveUpstreamCost(ctx, db, policy)
			require.NoError(t, err)
			require.NoError(t, model.ChargeUpstreamCeiling(ctx, db, r.ID))
			estimate := int64(5)
			input := model.SupplierCostInput{Account: "synthetic-account", Bill: "synthetic-invoice", Line: kind + "-concurrent", Currency: "USD", ReservationID: r.ID, ActualMicros: 4, EstimatedMicros: &estimate, Evidence: "synthetic-bill-evidence", Confirmed: true}
			var wg sync.WaitGroup
			failures := make(chan error, 8)
			for range 8 {
				wg.Go(func() {
					_, e := model.ImportSupplierCost(ctx, db, ch.Id, input)
					if e != nil {
						failures <- e
					}
				})
			}
			wg.Wait()
			close(failures)
			for e := range failures {
				require.NoError(t, e)
			}
			var count int64
			require.NoError(t, db.Model(&model.SupplierCostStatement{}).Where("channel_id = ?", ch.Id).Count(&count).Error)
			require.EqualValues(t, 1, count)
			var budget model.UpstreamBudget
			require.NoError(t, db.First(&budget, "channel_id = ?", ch.Id).Error)
			require.EqualValues(t, 4, budget.ChargedMicros)
			require.Zero(t, budget.HeldMicros)
			row, err := model.ImportSupplierCost(ctx, db, ch.Id, input)
			require.NoError(t, err)
			require.Equal(t, "settled", row.State)
			require.EqualValues(t, -1, *row.DeltaMicros)
			input.ActualMicros = 3
			_, err = model.ImportSupplierCost(ctx, db, ch.Id, input)
			require.ErrorIs(t, err, model.ErrSupplierCostConflict)
			require.NoError(t, db.First(&budget, "channel_id = ?", ch.Id).Error)
			require.True(t, budget.ReconciliationFrozen)
			require.False(t, budget.Enabled)
			require.EqualValues(t, 4, budget.ChargedMicros, "conflicting bill never overwrites booked cost")
			require.Error(t, model.ConfigureUpstreamBudget(ctx, db, policy, 1), "configuration cannot thaw a cost review")
			_, err = model.ReserveUpstreamCost(ctx, db, policy)
			require.Error(t, err)
			for _, scenario := range []string{"in_flight", "unmatched", "above_estimate", "above_ceiling", "missing_estimate", "rollback", "duplicate_attempt", "zero_actual"} {
				t.Run(scenario, func(t *testing.T) {
					channel, p := syntheticBudgetChannel(t, db)
					attempt, err := model.ReserveUpstreamCost(ctx, db, p)
					require.NoError(t, err)
					if scenario != "in_flight" {
						require.NoError(t, model.ChargeUpstreamCeiling(ctx, db, attempt.ID))
					}
					bill := input
					bill.Line, bill.ReservationID, bill.ActualMicros = kind+"-"+scenario, attempt.ID, 4
					switch scenario {
					case "unmatched":
						bill.ReservationID = strings.Repeat("e", 32)
					case "above_estimate":
						bill.ActualMicros = 8
					case "above_ceiling":
						bill.ActualMicros = 12
					case "missing_estimate":
						bill.EstimatedMicros = nil
					case "zero_actual":
						bill.ActualMicros = 0
					case "rollback":
						require.NoError(t, db.Callback().Update().Before("gorm:update").Register("synthetic-bill-write-failure", func(tx *gorm.DB) {
							if tx.Statement.Table == "supplier_cost_statements" {
								tx.AddError(errors.New("synthetic-bill-write-failure"))
							}
						}))
					case "duplicate_attempt":
						original := bill
						original.Line += "-original"
						_, err := model.ImportSupplierCost(ctx, db, channel.Id, original)
						require.NoError(t, err)
					}
					result, err := model.ImportSupplierCost(ctx, db, channel.Id, bill)
					if scenario == "rollback" {
						require.NoError(t, db.Callback().Update().Remove("synthetic-bill-write-failure"))
						require.Error(t, err)
						var saved model.UpstreamCostReservation
						require.NoError(t, db.First(&saved, "id = ?", attempt.ID).Error)
						require.Equal(t, "ceiling_charged", saved.State)
						return
					}
					require.NoError(t, err)
					var frozen model.UpstreamBudget
					require.NoError(t, db.First(&frozen, "channel_id = ?", channel.Id).Error)
					if scenario == "zero_actual" {
						require.Empty(t, result.Alert)
						require.Zero(t, frozen.ChargedMicros)
						require.Zero(t, frozen.HeldMicros)
						require.True(t, frozen.Enabled)
						return
					}
					require.NotEmpty(t, result.Alert)
					require.True(t, frozen.ReconciliationFrozen)
					require.False(t, frozen.Enabled)
					if scenario == "in_flight" {
						require.EqualValues(t, 10, frozen.HeldMicros, "a bill must not release a running attempt")
						require.NoError(t, model.ChargeUpstreamCeiling(ctx, db, attempt.ID))
						retry, err := model.ImportSupplierCost(ctx, db, channel.Id, bill)
						require.NoError(t, err)
						require.Equal(t, "settled", retry.State)
					}
				})
			}
			for _, invalid := range []string{"currency", "negative", "overflow", "unconfirmed"} {
				bad := input
				switch invalid {
				case "currency":
					bad.Currency = "CNY"
				case "negative":
					bad.ActualMicros = -1
				case "overflow":
					bad.ActualMicros = model.MaxUpstreamBudgetMicros + 1
				case "unconfirmed":
					bad.Confirmed = false
				}
				_, err := model.ImportSupplierCost(ctx, db, ch.Id, bad)
				require.Error(t, err)
			}
		})
	}
}

func TestSupplierCostLegacyReconcileRoutes(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			db := setupUpstreamBudgetDB(t, kind)
			require.NoError(t, db.AutoMigrate(&model.UpstreamBudget{}, &model.UpstreamCostReservation{}, &model.SupplierCostStatement{}, &model.User{}, &model.UserSession{}, &model.UserAccessToken{}, &model.AuditLog{}, &model.Option{}, &model.CasbinRule{}, &model.AuthzRole{}))
			require.NoError(t, authz.Init(db))
			master, secret := common.IsMasterNode, common.SessionSecret
			common.IsMasterNode, common.SessionSecret = true, "synthetic-legacy-review-session"
			t.Cleanup(func() { common.IsMasterNode, common.SessionSecret = master, secret })
			tokens := map[int]string{}
			fixtureID := rand.Text()[:8]
			for _, role := range []int{1, 10, 100} {
				user := model.User{Username: fmt.Sprintf("legacy-review-%s-%d-%s", kind, role, fixtureID), Role: role, Status: 1, AuthVersion: 1, Group: "default", AffCode: fmt.Sprintf("legacy-%s-%d-%s", kind, role, fixtureID)}
				require.NoError(t, db.Create(&user).Error)
				login, err := service.CreateLoginSession(user.Id, "synthetic", "127.0.0.1", "synthetic")
				require.NoError(t, err)
				tokens[role] = login.AccessToken
			}
			engine := gin.New()
			engine.Use(func(c *gin.Context) { c.Set(common.RequestIdKey, "synthetic-legacy-review"); c.Next() })
			registerChannelRoutes(engine.Group("/api"))
			for _, scenario := range []string{"no_bill", "missing_evidence", "in_flight", "over_ceiling", "already_frozen", "conflicting_bill", "new_then_old", "new_over_ceiling", "concurrent_new_and_old", "historical"} {
				t.Run(scenario, func(t *testing.T) {
					ch, policy := syntheticBudgetChannel(t, db)
					ctx := context.Background()
					attempt, err := model.ReserveUpstreamCost(ctx, db, policy)
					require.NoError(t, err)
					if scenario != "in_flight" {
						require.NoError(t, model.ChargeUpstreamCeiling(ctx, db, attempt.ID))
					}
					base := fmt.Sprintf("/api/channel/%d/upstream-budget", ch.Id)
					// Requests use the public management router and real signed sessions.
					send := func(method, suffix string, input any, role int) *httptest.ResponseRecorder {
						data, err := common.Marshal(input)
						require.NoError(t, err)
						request := httptest.NewRequest(method, base+suffix, strings.NewReader(string(data)))
						request.Header.Set("Content-Type", "application/json")
						if role != 0 {
							request.Header.Set("Authorization", "Bearer "+tokens[role])
						}
						response := httptest.NewRecorder()
						engine.ServeHTTP(response, request)
						return response
					}
					estimate := int64(5)
					bill := model.SupplierCostInput{Account: "synthetic-private-legacy-account", Bill: kind + "-" + scenario, Line: "one", Currency: "USD", ReservationID: attempt.ID, ActualMicros: 4, EstimatedMicros: &estimate, Evidence: "synthetic-private-legacy-evidence", Confirmed: true}
					if scenario == "conflicting_bill" || scenario == "new_then_old" || scenario == "new_over_ceiling" {
						// Keep a second obligation to prove that a conflict on one bill
						// cannot be bypassed by settling another attempt via the old API.
						var peer model.UpstreamCostReservation
						if scenario == "conflicting_bill" {
							peer, err = model.ReserveUpstreamCost(ctx, db, policy)
							require.NoError(t, err)
							require.NoError(t, model.ChargeUpstreamCeiling(ctx, db, peer.ID))
						}
						if scenario == "new_over_ceiling" {
							bill.ActualMicros = 12
						}
						require.Equal(t, 200, send("POST", "/statements", bill, 100).Code)
						require.Equal(t, 200, send("POST", "/statements", bill, 100).Code, "bill replay is idempotent")
						if scenario == "conflicting_bill" {
							bill.ActualMicros = 5
							require.Equal(t, 409, send("POST", "/statements", bill, 100).Code)
							attempt = peer
						}
					}
					if scenario == "already_frozen" {
						require.NoError(t, db.Model(&model.UpstreamBudget{}).Where("channel_id = ?", ch.Id).Updates(map[string]any{"enabled": false, "reconciliation_frozen": true}).Error)
					}
					if scenario == "historical" {
						// Seed a pre-F2.1 settlement as historical data, not a new API write.
						require.NoError(t, model.ReconcileUpstreamCost(ctx, db, attempt.ID, 4, bill.Evidence))
					}
					old := map[string]any{"reservation_id": attempt.ID, "actual_micros": 0, "evidence": "synthetic-legacy-unverified-proof"}
					if scenario == "missing_evidence" {
						delete(old, "evidence")
					}
					if scenario == "over_ceiling" {
						old["actual_micros"] = 12
					}
					if scenario == "historical" {
						old["actual_micros"], old["evidence"] = 4, bill.Evidence
					}
					for _, role := range []int{0, 1, 10} {
						response := send("POST", "/reconcile", old, role)
						expected := 403
						if role == 0 {
							expected = 401
						}
						require.Equal(t, expected, response.Code)
					}
					if scenario == "concurrent_new_and_old" {
						type result struct {
							legacy bool
							status int
						}
						results := make(chan result, 4)
						var wg sync.WaitGroup
						for index := range 4 {
							wg.Go(func() {
								if index%2 == 0 {
									results <- result{true, send("POST", "/reconcile", old, 100).Code}
								} else {
									results <- result{false, send("POST", "/statements", bill, 100).Code}
								}
							})
						}
						wg.Wait()
						close(results)
						for result := range results {
							if result.legacy {
								require.Equal(t, 410, result.status)
							} else {
								require.Equal(t, 200, result.status)
							}
						}
					}
					var before, after model.UpstreamBudget
					var beforeAttempt, afterAttempt model.UpstreamCostReservation
					var beforeCount, afterCount int64
					require.NoError(t, db.First(&before, "channel_id = ?", ch.Id).Error)
					require.NoError(t, db.First(&beforeAttempt, "id = ?", attempt.ID).Error)
					require.NoError(t, db.Model(&model.SupplierCostStatement{}).Where("channel_id = ?", ch.Id).Count(&beforeCount).Error)
					if scenario == "new_then_old" || scenario == "concurrent_new_and_old" {
						require.EqualValues(t, 4, before.ChargedMicros)
						require.Zero(t, before.HeldMicros)
						require.EqualValues(t, 1, beforeCount, "canonical bill is booked exactly once")
					}
					if scenario == "new_over_ceiling" {
						require.EqualValues(t, 12, before.ChargedMicros)
						require.True(t, before.ReconciliationFrozen)
						require.False(t, before.Enabled)
					}
					response := send("POST", "/reconcile", old, 100)
					require.NoError(t, db.First(&after, "channel_id = ?", ch.Id).Error)
					require.NoError(t, db.First(&afterAttempt, "id = ?", attempt.ID).Error)
					require.NoError(t, db.Model(&model.SupplierCostStatement{}).Where("channel_id = ?", ch.Id).Count(&afterCount).Error)
					t.Logf("legacy route status=%d held=%d->%d allocated=%d->%d bill_rows=%d->%d review_frozen=%v->%v", response.Code, before.HeldMicros, after.HeldMicros, before.ChargedMicros, after.ChargedMicros, beforeCount, afterCount, before.ReconciliationFrozen, after.ReconciliationFrozen)
					require.Equal(t, 410, response.Code, response.Body.String())
					require.Contains(t, response.Body.String(), "SUPPLIER_BILL_REQUIRED")
					require.Equal(t, before, after)
					require.Equal(t, beforeAttempt, afterAttempt, "history and obligations are immutable through the old API")
					require.Equal(t, beforeCount, afterCount)
					if before.ReconciliationFrozen {
						configuration, err := common.Marshal(policy)
						require.NoError(t, err)
						var update map[string]any
						require.NoError(t, common.Unmarshal(configuration, &update))
						update["expected_revision"], update["reconciliation_frozen"], update["cost_basis"] = before.Revision, false, "synthetic-reviewed-cost-basis"
						require.Equal(t, 409, send("PUT", "", update, 100).Code, "configuration cannot remove a review freeze")
					}
					var audit model.AuditLog
					require.NoError(t, db.Where("action = ?", "upstream_budget.reconcile").Order("id DESC").First(&audit).Error)
					require.Equal(t, 410, audit.Status)
					require.False(t, audit.Success)
					auditData, err := common.Marshal(audit)
					require.NoError(t, err)
					require.Contains(t, string(auditData), fmt.Sprintf(`"channel_id":%d`, ch.Id))
					for _, secret := range []string{tokens[100], bill.Evidence, "synthetic-legacy-unverified-proof"} {
						require.NotContains(t, string(auditData), secret)
					}
					view := send("GET", "", nil, 100)
					require.Equal(t, 200, view.Code)
					require.Contains(t, view.Body.String(), attempt.ID)
					if scenario == "historical" {
						require.Contains(t, view.Body.String(), `"state":"reconciled"`)
					}
				})
			}
		})
	}
}

func TestAggregateCostPublicationDatabaseMatrix(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			db := setupUpstreamBudgetDB(t, kind)
			// Upgrade the released schema twice, without deleting old financial rows.
			require.NoError(t, db.AutoMigrate(&model.UpstreamBudget{}, &model.UpstreamCostReservation{}))
			channel, policy := syntheticBudgetChannel(t, db)
			reservation, err := model.ReserveUpstreamCost(context.Background(), db, policy)
			require.NoError(t, err)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.AggregateCostHead{}, &model.AggregateCostVersion{}))
			}
			end := time.Now().Unix() - 10
			start := end - 100
			snapshot := service.CostSnapshot{Complete: true, Buckets: []service.CostBucket{{Organization: "synthetic-org", Project: "synthetic-" + kind, Start: start, End: end, USDmicros: 7}}}
			publish := func(expected int64, s service.CostSnapshot) (model.AggregateCostVersion, error) {
				return service.PublishMockCostSnapshot(context.Background(), db, "synthetic-org", "synthetic-"+kind, start, end, expected, s)
			}
			first, err := publish(0, snapshot)
			require.NoError(t, err)
			replay, err := publish(0, snapshot)
			require.NoError(t, err)
			require.Equal(t, first.ID, replay.ID)
			changed := snapshot
			changed.Buckets = append([]service.CostBucket(nil), snapshot.Buckets...)
			changed.Buckets[0].USDmicros = 8
			var wg sync.WaitGroup
			successes := make(chan model.AggregateCostVersion, 8)
			for range 8 {
				wg.Go(func() {
					r, e := publish(1, changed)
					if e == nil {
						successes <- r
					}
				})
			}
			wg.Wait()
			close(successes)
			for row := range successes {
				require.EqualValues(t, 2, row.Revision)
			}
			var count int64
			require.NoError(t, db.Model(&model.AggregateCostVersion{}).Where("scope = ?", first.Scope).Count(&count).Error)
			require.EqualValues(t, 2, count)
			var old model.AggregateCostVersion
			require.NoError(t, db.First(&old, "id = ?", first.ID).Error)
			require.Equal(t, first.Payload, old.Payload)
			third := changed
			third.Buckets = append([]service.CostBucket(nil), changed.Buckets...)
			third.Buckets[0].USDmicros = 9
			_, err = publish(1, third)
			require.Error(t, err, "stale writers cannot overwrite current version")
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("synthetic-version-failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "aggregate_cost_versions" {
					tx.AddError(errors.New("synthetic failure"))
				}
			}))
			_, err = publish(2, third)
			require.Error(t, err)
			require.NoError(t, db.Callback().Create().Remove("synthetic-version-failure"))
			var head model.AggregateCostHead
			require.NoError(t, db.First(&head, "scope = ?", first.Scope).Error)
			require.EqualValues(t, 2, head.Revision)
			invalid := snapshot
			invalid.Buckets = append(invalid.Buckets, snapshot.Buckets[0])
			_, err = publish(2, invalid)
			require.Error(t, err, "forged Complete cannot bypass revalidation")
			var after model.UpstreamBudget
			require.NoError(t, db.First(&after, "channel_id = ?", channel.Id).Error)
			require.EqualValues(t, 10, after.HeldMicros)
			require.Zero(t, after.ChargedMicros)
			var held model.UpstreamCostReservation
			require.NoError(t, db.First(&held, "id = ?", reservation.ID).Error)
			require.Equal(t, "held", held.State)
		})
	}
}
