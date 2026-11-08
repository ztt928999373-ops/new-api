// Copyright (c) QuantumNous and contributors.
package controller

import (
	"context"
	"io"
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
)

type credentialAPITestTransport func(*http.Request) (*http.Response, error)

func (f credentialAPITestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSupplierCredentialManagementNeverReturnsPlaintext(t *testing.T) {
	setupTaskPluginBindChannelTest(t)
	keyDir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, credentialcrypto.InitializeLocalDirectory(keyDir))
	path := filepath.Join(keyDir, "synthetic-ring")
	require.NoError(t, credentialcrypto.InitializeLocalFile(path))
	p, err := credentialcrypto.NewLocalFileKeyProvider(path)
	require.NoError(t, err)
	s := &credentials.Store{DB: model.DB, Environment: "test", ProviderID: "local-test", Provider: p}
	require.NoError(t, s.MigrateSchema())
	old := service.SetChannelCredentialResolver(&credentials.Resolver{Environment: "test", Repository: s, Providers: map[string]credentialcrypto.KeyProvider{"local-test": p}, Audit: s})
	defer service.SetChannelCredentialResolver(old)
	call := func(handler gin.HandlerFunc, id, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("id", 1)
		c.Set("role", common.RoleRootUser)
		c.Params = gin.Params{{Key: "id", Value: id}}
		c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/credentials", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		handler(c)
		require.NotContains(t, w.Body.String(), "synthetic-secret")
		return w
	}
	w := call(CreateSupplierCredential, "", `{"key":"synthetic-secret-one","owner_id":"victim"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var response struct {
		Data credentials.Head `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &response))
	id := response.Data.ID
	require.NotEmpty(t, id)
	meta, err := s.Metadata(context.Background(), credentials.Principal{TenantID: "default", UserID: "1", PlatformOperator: true}, id)
	require.NoError(t, err)
	require.Equal(t, "platform", meta.OwnerType)
	w = call(ReplaceSupplierCredential, id, `{"key":"synthetic-secret-two","revision":1}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = call(GetSupplierCredential, id, "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"versions":[1,2]`)
	service.InitHttpClient()
	client := service.GetHttpClient()
	oldTransport := client.Transport
	defer func() { client.Transport = oldTransport }()
	client.Transport = credentialAPITestTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "https://api.openai.com/v1/models", r.URL.String())
		require.Equal(t, "Bearer synthetic-secret-two", r.Header.Get("Authorization"))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("synthetic-secret-upstream-body")), Header: make(http.Header)}, nil
	})
	w = call(TestSupplierCredential, id, "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"model_verified":false`)
	w = call(SetSupplierCredentialStatus, id, `{"status":"disabled","revision":2}`)
	require.Equal(t, http.StatusOK, w.Code)
	w = call(TestSupplierCredential, id, "")
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	w = call(SetSupplierCredentialStatus, id, `{"status":"active","revision":3}`)
	require.Equal(t, http.StatusOK, w.Code)
	victim, err := s.Create(context.Background(), credentials.Principal{TenantID: "default", UserID: "other-user"}, false, "openai", "api_key", "relay", []byte("synthetic-secret-victim"))
	require.NoError(t, err)
	w = call(GetSupplierCredential, victim.ID, "")
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}
