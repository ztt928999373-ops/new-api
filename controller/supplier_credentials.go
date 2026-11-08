// Copyright (c) QuantumNous and contributors.
package controller

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/credentials"
	"github.com/gin-gonic/gin"
)

// Platform ownership is derived here, never accepted from request JSON.
func platformCredentialActor(c *gin.Context) credentials.Principal {
	return credentials.Principal{TenantID: "default", UserID: strconv.Itoa(c.GetInt("id")), PlatformOperator: true}
}
func credentialFailure(c *gin.Context) {
	message := common.NewMessage("Invalid request")
	fields := message.Fields()
	fields["success"] = false
	fields["message"] = message.Error()
	c.JSON(http.StatusServiceUnavailable, fields)
}

func CreateSupplierCredential(c *gin.Context) {
	s := service.ConfiguredCredentialStore()
	if s == nil || c.GetInt("id") <= 0 {
		credentialFailure(c)
		return
	}
	var input struct {
		Key string `json:"key"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Key == "" {
		credentialFailure(c)
		return
	}
	h, err := s.Create(c.Request.Context(), platformCredentialActor(c), true, "openai", "api_key", "relay", []byte(input.Key))
	if err != nil {
		credentialFailure(c)
		return
	}
	recordManageAudit(c, "credential.create", map[string]any{"id": h.ID})
	c.JSON(http.StatusOK, gin.H{"success": true, "data": h})
}
func GetSupplierCredential(c *gin.Context) {
	s := service.ConfiguredCredentialStore()
	if s == nil {
		credentialFailure(c)
		return
	}
	h, err := s.Metadata(c.Request.Context(), platformCredentialActor(c), c.Param("id"))
	if err != nil {
		credentialFailure(c)
		return
	}
	versions, err := s.Versions(c.Request.Context(), platformCredentialActor(c), h.ID)
	if err != nil {
		credentialFailure(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": h, "versions": versions})
}
func ReplaceSupplierCredential(c *gin.Context) {
	s := service.ConfiguredCredentialStore()
	if s == nil {
		credentialFailure(c)
		return
	}
	var input struct {
		Key      string `json:"key"`
		Revision int64  `json:"revision"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Revision <= 0 {
		credentialFailure(c)
		return
	}
	h, err := s.Replace(c.Request.Context(), platformCredentialActor(c), c.Param("id"), input.Revision, []byte(input.Key))
	if err != nil {
		credentialFailure(c)
		return
	}
	recordManageAudit(c, "credential.replace", map[string]any{"id": h.ID, "version": h.CurrentVersion})
	c.JSON(http.StatusOK, gin.H{"success": true, "data": h})
}
func SetSupplierCredentialStatus(c *gin.Context) {
	s := service.ConfiguredCredentialStore()
	if s == nil {
		credentialFailure(c)
		return
	}
	var input struct {
		Status   string `json:"status"`
		Revision int64  `json:"revision"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Revision <= 0 {
		credentialFailure(c)
		return
	}
	if s.SetStatus(c.Request.Context(), platformCredentialActor(c), c.Param("id"), input.Revision, input.Status) != nil {
		credentialFailure(c)
		return
	}
	recordManageAudit(c, "credential.status", map[string]any{"id": c.Param("id"), "status": input.Status})
	c.JSON(http.StatusOK, gin.H{"success": true})
}
func TestSupplierCredential(c *gin.Context) {
	s := service.ConfiguredCredentialStore()
	if s == nil {
		credentialFailure(c)
		return
	}
	actor := platformCredentialActor(c)
	h, err := s.Metadata(c.Request.Context(), actor, c.Param("id"))
	if err != nil || h.Provider != "openai" || h.CredentialType != "api_key" {
		credentialFailure(c)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	err = service.WithConfiguredCredentialContext(ctx, actor, h.ID, uint64(h.CurrentVersion), func(useCtx context.Context, secret []byte) error {
		req, err := http.NewRequestWithContext(useCtx, http.MethodGet, "https://api.openai.com/v1/models", nil)
		if err != nil {
			return credentialcrypto.ErrUnavailable
		}
		req.Header.Set("Authorization", "Bearer "+string(secret))
		base := service.GetHttpClient()
		if base == nil {
			return credentialcrypto.ErrUnavailable
		}
		client := *base
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := client.Do(req)
		if err != nil {
			return credentialcrypto.ErrUnavailable
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
		if resp.StatusCode != http.StatusOK {
			return credentialcrypto.ErrUnavailable
		}
		return nil
	})
	if err != nil {
		credentialFailure(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "connected": true, "model_verified": false})
}

func RewrapSupplierCredential(c *gin.Context) {
	s := service.ConfiguredCredentialStore()
	if s == nil {
		credentialFailure(c)
		return
	}
	var input struct {
		Version  int64 `json:"version"`
		Revision int64 `json:"revision"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Version <= 0 || input.Revision <= 0 {
		credentialFailure(c)
		return
	}
	if s.RewrapVersion(c.Request.Context(), platformCredentialActor(c), c.Param("id"), input.Version, input.Revision) != nil {
		credentialFailure(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}
