package controller

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterDisabledRejectsBeforeParsingAndPreservesLogin(t *testing.T) {
	user, _ := setupSecurityEnrollmentTest(t)
	previousRegister := common.RegisterEnabled
	common.RegisterEnabled = false
	t.Cleanup(func() { common.RegisterEnabled = previousRegister })

	var usersBefore int64
	require.NoError(t, model.DB.Model(&model.User{}).Count(&usersBefore).Error)

	engine := gin.New()
	engine.POST("/api/user/register", Register)
	engine.POST("/api/user/login", Login)
	engine.GET("/api/status", GetStatus)

	for _, body := range []string{
		`{}`,
		`{"username":"new-user","password":"Synthetic-Password!2026","email":"new-user@example.test"}`,
	} {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest("POST", "/api/user/register", strings.NewReader(body)))
		assert.Contains(t, response.Body.String(), "New user registration has been disabled")
	}

	var usersAfter int64
	require.NoError(t, model.DB.Model(&model.User{}).Count(&usersAfter).Error)
	assert.Equal(t, usersBefore, usersAfter)
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}))
	var tokens int64
	require.NoError(t, model.DB.Model(&model.Token{}).Count(&tokens).Error)
	assert.Zero(t, tokens)

	status := httptest.NewRecorder()
	engine.ServeHTTP(status, httptest.NewRequest("GET", "/api/status", nil))
	assert.Contains(t, status.Body.String(), `"register_enabled":false`)

	login := httptest.NewRecorder()
	engine.ServeHTTP(login, httptest.NewRequest("POST", "/api/user/login", strings.NewReader(`{"username":"`+user.Username+`","password":"enrollment-password"}`)))
	assert.Contains(t, login.Body.String(), `"success":true`)
}
