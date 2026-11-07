// Copyright (c) QuantumNous and contributors.
package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIPreparationPersistsDisabledSingleModelWithoutEcho(t *testing.T) {
	setupTaskPluginBindChannelTest(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("role", common.RoleRootUser)
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
	require.Equal(t, "synthetic-pilot-key", saved.Key)
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
