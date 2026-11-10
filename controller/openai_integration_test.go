// Copyright (c) QuantumNous and contributors.
package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/credentials"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOpenAIPreparationPersistsDisabledSingleModelWithoutEcho(t *testing.T) {
	setupTaskPluginBindChannelTest(t)
	dir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, credentialcrypto.InitializeLocalDirectory(dir))
	path := filepath.Join(dir, "ring")
	require.NoError(t, credentialcrypto.InitializeLocalFile(path))
	p, err := credentialcrypto.NewLocalFileKeyProvider(path)
	require.NoError(t, err)
	store := &credentials.Store{DB: model.DB, Environment: "test", ProviderID: "local-test", Provider: p}
	require.NoError(t, store.MigrateSchema())
	old := service.SetChannelCredentialResolver(&credentials.Resolver{Environment: "test", Repository: store, Providers: map[string]credentialcrypto.KeyProvider{"local-test": p}, Audit: store})
	t.Cleanup(func() { service.SetChannelCredentialResolver(old) })
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("role", common.RoleRootUser)
	c.Set("id", 1)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/openai-integration", strings.NewReader(`{"name":"pilot","model":"pilot-model","key":"synthetic-pilot-key","base_url":"http://127.0.0.1","status":1,"group":"default"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	PrepareOpenAIIntegration(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"success":true`)
	require.NotContains(t, w.Body.String(), "synthetic-pilot-key")
	var saved model.Channel
	require.NoError(t, model.DB.First(&saved).Error)
	require.Equal(t, "https://api.openai.com", *saved.BaseURL)
	require.Equal(t, "openai-pilot", saved.Group)
	require.Equal(t, common.ChannelStatusManuallyDisabled, saved.Status)
	require.Equal(t, "pilot-model", saved.Models)
	require.Empty(t, saved.Key)
	require.NotEmpty(t, saved.CredentialRef)
	require.NoError(t, service.WithConfiguredCredentialContext(c.Request.Context(), platformCredentialActor(c), saved.CredentialRef, 1, func(_ context.Context, secret []byte) error {
		require.Equal(t, "synthetic-pilot-key", string(secret))
		return nil
	}))
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register("test:pilot-ability-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "abilities" {
			tx.AddError(errors.New("synthetic failure"))
		}
	}))
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Set("id", 1)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/openai-integration", strings.NewReader(`{"name":"rollback","model":"pilot-model","key":"synthetic-rollback"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	PrepareOpenAIIntegration(c)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.NoError(t, model.DB.Callback().Create().Remove("test:pilot-ability-failure"))
	var channels, heads int64
	require.NoError(t, model.DB.Model(&model.Channel{}).Count(&channels).Error)
	require.NoError(t, model.DB.Model(&credentials.Head{}).Count(&heads).Error)
	require.EqualValues(t, 1, channels)
	require.EqualValues(t, 1, heads)
}

func TestOpenAIPreparationRejectsInvalidCredentialsWithoutEcho(t *testing.T) {
	for _, body := range []string{
		`{"name":"pilot","model":"one,two","key":"synthetic-secret"}`,
		`{"name":"pilot","model":"one","key":"synthetic-secret\nsecond"}`,
		`{"name":"pilot","model":"one","key":""}`,
		`{"name":"pilot","model":"one","key":123}`,
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/openai-integration", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		PrepareOpenAIIntegration(c)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.NotContains(t, w.Body.String(), "synthetic-secret")
	}
}

func TestOpenAIPreparationFailsClosedWithoutResolver(t *testing.T) {
	previous := service.SetChannelCredentialResolver(nil)
	t.Cleanup(func() { service.SetChannelCredentialResolver(previous) })
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("id", 1)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/openai-integration", strings.NewReader(`{"name":"pilot","model":"one","key":"synthetic-unavailable"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	PrepareOpenAIIntegration(c)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.NotContains(t, w.Body.String(), "synthetic-unavailable")
}

func TestBailianPreparationPersistsDisabledSingleModelWithoutEcho(t *testing.T) {
	setupTaskPluginBindChannelTest(t)
	dir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, credentialcrypto.InitializeLocalDirectory(dir))
	path := filepath.Join(dir, "ring")
	require.NoError(t, credentialcrypto.InitializeLocalFile(path))
	p, err := credentialcrypto.NewLocalFileKeyProvider(path)
	require.NoError(t, err)
	store := &credentials.Store{DB: model.DB, Environment: "test", ProviderID: "local-test", Provider: p}
	require.NoError(t, store.MigrateSchema())
	old := service.SetChannelCredentialResolver(&credentials.Resolver{Environment: "test", Repository: store, Providers: map[string]credentialcrypto.KeyProvider{"local-test": p}, Audit: store})
	t.Cleanup(func() { service.SetChannelCredentialResolver(old) })
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("role", common.RoleRootUser)
	c.Set("id", 1)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/bailian-integration", strings.NewReader(`{"name":"pilot","model":"qwen3.7-flash-2026-07-15","key":"synthetic-pilot-key","base_url":"http://127.0.0.1","status":1,"group":"default"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	PrepareBailianIntegration(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"success":true`)
	require.NotContains(t, w.Body.String(), "synthetic-pilot-key")
	var saved model.Channel
	require.NoError(t, model.DB.First(&saved).Error)
	require.Equal(t, "https://dashscope.aliyuncs.com", *saved.BaseURL)
	require.Equal(t, "bailian-pilot", saved.Group)
	require.Equal(t, common.ChannelStatusManuallyDisabled, saved.Status)
	require.Equal(t, "qwen3.7-flash-2026-07-15", saved.Models)
	require.Empty(t, saved.Key)
	require.NotEmpty(t, saved.CredentialRef)
	for _, tc := range []struct {
		name   string
		mutate func(*model.Channel)
	}{
		{"valid", func(ch *model.Channel) {}},
		{"http", func(ch *model.Channel) { v := "http://dashscope.aliyuncs.com"; ch.BaseURL = &v }},
		{"suffix", func(ch *model.Channel) { v := "https://dashscope.aliyuncs.com.evil.invalid"; ch.BaseURL = &v }},
		{"query", func(ch *model.Channel) { v := "https://dashscope.aliyuncs.com?x=y"; ch.BaseURL = &v }},
		{"other_region", func(ch *model.Channel) { v := "https://dashscope-intl.aliyuncs.com"; ch.BaseURL = &v }},
		{"openai_type", func(ch *model.Channel) { ch.Type = 1 }},
		{"model", func(ch *model.Channel) { ch.Models = "other-model" }},
		{"override", func(ch *model.Channel) { v := `{"Authorization":"secret"}`; ch.HeaderOverride = &v }},
		{"mapping", func(ch *model.Channel) { v := `{"qwen3.7-flash-2026-07-15":"other"}`; ch.ModelMapping = &v }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := saved
			tc.mutate(&candidate)
			relay, _ := gin.CreateTestContext(httptest.NewRecorder())
			relay.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			bound, err := service.BindChannelCredentialRequest(relay, &candidate, 1)
			defer service.ReleaseChannelCredentialRequest(relay)
			if tc.name == "valid" {
				require.NoError(t, err)
				require.Equal(t, "synthetic-pilot-key", bound.Key)
			} else {
				require.Error(t, err)
				require.Nil(t, bound)
			}
			require.Empty(t, saved.Key)
		})
	}

	require.NoError(t, service.WithConfiguredCredentialContext(c.Request.Context(), platformCredentialActor(c), saved.CredentialRef, 1, func(_ context.Context, secret []byte) error {
		require.Equal(t, "synthetic-pilot-key", string(secret))
		return nil
	}))
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register("test:pilot-ability-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "abilities" {
			tx.AddError(errors.New("synthetic failure"))
		}
	}))
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Set("id", 1)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/bailian-integration", strings.NewReader(`{"name":"rollback","model":"qwen3.7-flash-2026-07-15","key":"synthetic-rollback"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	PrepareBailianIntegration(c)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.NoError(t, model.DB.Callback().Create().Remove("test:pilot-ability-failure"))
	var channels, heads int64
	require.NoError(t, model.DB.Model(&model.Channel{}).Count(&channels).Error)
	require.NoError(t, model.DB.Model(&credentials.Head{}).Count(&heads).Error)
	require.EqualValues(t, 1, channels)
	require.EqualValues(t, 1, heads)
}

func TestBailianPreparationRejectsInvalidCredentialsWithoutEcho(t *testing.T) {
	for _, body := range []string{
		`{"name":"pilot","model":"one,two","key":"synthetic-secret"}`,
		`{"name":"pilot","model":"one","key":"synthetic-secret\nsecond"}`,
		`{"name":"pilot","model":"one","key":""}`,
		`{"name":"pilot","model":"one","key":123}`,
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/bailian-integration", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		PrepareBailianIntegration(c)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.NotContains(t, w.Body.String(), "synthetic-secret")
	}
}

func TestBailianPreparationFailsClosedWithoutResolver(t *testing.T) {
	previous := service.SetChannelCredentialResolver(nil)
	t.Cleanup(func() { service.SetChannelCredentialResolver(previous) })
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("id", 1)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/bailian-integration", strings.NewReader(`{"name":"pilot","model":"qwen3.7-flash-2026-07-15","key":"synthetic-unavailable"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	PrepareBailianIntegration(c)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.NotContains(t, w.Body.String(), "synthetic-unavailable")
}
