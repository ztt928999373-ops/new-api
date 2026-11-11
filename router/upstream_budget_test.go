package router

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/service/credentials"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupUpstreamBudgetDB(t *testing.T, kind string) *gorm.DB {
	t.Helper()
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

func TestUpstreamBudgetAuthenticatedRetryStopsBeforeSecondSend(t *testing.T) {
	db := setupUpstreamBudgetDB(t, "sqlite")
	require.NoError(t, db.AutoMigrate(&model.UpstreamBudget{}, &model.UpstreamCostReservation{}, &model.User{}, &model.Token{}, &model.Ability{}, &model.Channel{}, &model.Option{}, &model.Log{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}))
	ch, p := syntheticBudgetChannel(t, db)
	p.LimitMicros = 10
	require.NoError(t, model.ConfigureUpstreamBudget(context.Background(), db, p, 1))
	p.Revision = 2
	user := model.User{Username: "synthetic-budget-client", Role: 1, Status: 1, Group: "default", Quota: 1000000, AffCode: "budget-client"}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "syntheticbudgetclient", Status: 1, ExpiredTime: -1, RemainQuota: 1000000}
	require.NoError(t, db.Create(&token).Error)
	keyDir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, credentialcrypto.InitializeLocalDirectory(keyDir))
	keyPath := filepath.Join(keyDir, "ring")
	require.NoError(t, credentialcrypto.InitializeLocalFile(keyPath))
	provider, err := credentialcrypto.NewLocalFileKeyProvider(keyPath)
	require.NoError(t, err)
	store := &credentials.Store{DB: db, Environment: "test", ProviderID: "local-test", Provider: provider}
	require.NoError(t, store.MigrateSchema())
	head, err := store.Create(context.Background(), credentials.Principal{TenantID: "default", UserID: fmt.Sprint(user.Id), PlatformOperator: true}, true, "openai", "api_key", "relay", []byte("synthetic-budget-supplier"))
	require.NoError(t, err)
	oldResolver := service.SetChannelCredentialResolver(&credentials.Resolver{Environment: "test", Repository: store, Providers: map[string]credentialcrypto.KeyProvider{"local-test": provider}, Audit: store})
	t.Cleanup(func() { service.SetChannelCredentialResolver(oldResolver) })
	require.NoError(t, db.Model(&ch).Updates(map[string]any{"credential_ref": head.ID, "models": "synthetic-model", "group": "default"}).Error)
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
	engine := gin.New()
	SetRelayRouter(engine)
	for range 2 {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"synthetic-model","messages":[{"role":"user","content":"hi"}],"max_tokens":16}`))
		req.Header.Set("Authorization", "Bearer syntheticbudgetclient")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		require.GreaterOrEqual(t, w.Code, 400, w.Body.String())
		require.NotContains(t, w.Body.String(), "synthetic-budget-supplier")
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
		})
	}
}

func TestUpstreamBudgetHTTPAttemptsAndPermissions(t *testing.T) {
	db := setupUpstreamBudgetDB(t, "sqlite")
	require.NoError(t, db.AutoMigrate(&model.UpstreamBudget{}, &model.UpstreamCostReservation{}, &model.User{}, &model.UserSession{}, &model.UserAccessToken{}, &model.Option{}, &model.AuditLog{}, &model.CasbinRule{}, &model.AuthzRole{}))
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
		for _, method := range []string{"GET", "PUT", "POST"} {
			path := fmt.Sprintf("/api/channel/%d/upstream-budget", ch.Id)
			if method == "POST" {
				path += "/reconcile"
			}
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
	// Default/off is an explicit opt-out and retains existing channel behavior.
	p.Enabled = false
	p.ValidUntil = time.Now().Unix() + 3600
	require.NoError(t, model.ConfigureUpstreamBudget(context.Background(), db, p, p.Revision))
	r, err := send(`{"legacy":"unrestricted-by-this-feature"}`)
	require.NoError(t, err)
	require.NoError(t, r.Body.Close())
	require.EqualValues(t, 11, calls.Load())
	state = model.UpstreamBudget{}
	require.NoError(t, db.First(&state, "channel_id = ?", ch.Id).Error)
	require.EqualValues(t, 100, state.ChargedMicros)
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
