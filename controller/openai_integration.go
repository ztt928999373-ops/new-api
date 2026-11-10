// Copyright (c) QuantumNous and contributors.
package controller

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// PrepareOpenAIIntegration atomically creates an encrypted credential and a
// disabled single-model channel. Plaintext never enters the channel row.
func PrepareOpenAIIntegration(c *gin.Context) {
	var input struct {
		Name  string `json:"name"`
		Model string `json:"model"`
		Key   string `json:"key"`
	}
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid request"})
		return
	}
	input.Name, input.Model = strings.TrimSpace(input.Name), strings.TrimSpace(input.Model)
	if input.Name == "" || len(input.Name) > 128 || input.Model == "" || len(input.Model) > 128 ||
		strings.ContainsAny(input.Model, ",\r\n\t ") || input.Key == "" || len(input.Key) > 512 || strings.ContainsAny(input.Key, "\r\n\t ") {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid request"})
		return
	}
	baseURL := "https://api.openai.com"
	store := service.ConfiguredCredentialStore()
	if store == nil || c.GetInt("id") <= 0 {
		credentialFailure(c)
		return
	}
	channel := model.Channel{
		Type: constant.ChannelTypeOpenAI, Name: input.Name,
		Models: input.Model, Group: "openai-pilot", BaseURL: &baseURL,
		Status: common.ChannelStatusManuallyDisabled,
	}
	err := store.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		local := *store
		local.DB = tx
		head, err := local.Create(c.Request.Context(), platformCredentialActor(c), true, "openai", "api_key", "relay", []byte(input.Key))
		if err != nil {
			return err
		}
		channel.CredentialRef = head.ID
		if err := tx.Create(&channel).Error; err != nil {
			return err
		}
		return channel.AddAbilities(tx)
	})
	if err != nil {
		credentialFailure(c)
		return
	}
	recordManageAudit(c, "channel.openai_prepare", map[string]any{"id": channel.Id})
	if err := model.InitChannelCache(); err != nil {
		channelCacheSyncFailure(c, channel.Id)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"id": channel.Id}})
}

// PrepareBailianIntegration atomically creates an encrypted Beijing credential and a
// disabled single-model channel. Plaintext never enters the channel row.
func PrepareBailianIntegration(c *gin.Context) {
	var input struct {
		Name  string `json:"name"`
		Model string `json:"model"`
		Key   string `json:"key"`
	}
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid request"})
		return
	}
	input.Name, input.Model = strings.TrimSpace(input.Name), strings.TrimSpace(input.Model)
	if input.Name == "" || len(input.Name) > 128 || input.Model == "" || len(input.Model) > 128 ||
		strings.ContainsAny(input.Model, ",\r\n\t ") || input.Key == "" || len(input.Key) > 512 || strings.ContainsAny(input.Key, "\r\n\t ") {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid request"})
		return
	}
	if input.Model != "qwen3.7-flash-2026-07-15" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid request"})
		return
	}
	baseURL := "https://dashscope.aliyuncs.com"
	store := service.ConfiguredCredentialStore()
	if store == nil || c.GetInt("id") <= 0 {
		credentialFailure(c)
		return
	}
	channel := model.Channel{
		Type: constant.ChannelTypeAli, Name: input.Name,
		Models: input.Model, Group: "bailian-pilot", BaseURL: &baseURL,
		Status: common.ChannelStatusManuallyDisabled,
	}
	err := store.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		local := *store
		local.DB = tx
		head, err := local.Create(c.Request.Context(), platformCredentialActor(c), true, "bailian", "api_key", "relay", []byte(input.Key))
		if err != nil {
			return err
		}
		channel.CredentialRef = head.ID
		if err := tx.Create(&channel).Error; err != nil {
			return err
		}
		return channel.AddAbilities(tx)
	})
	if err != nil {
		credentialFailure(c)
		return
	}
	recordManageAudit(c, "channel.bailian_prepare", map[string]any{"id": channel.Id})
	if err := model.InitChannelCache(); err != nil {
		channelCacheSyncFailure(c, channel.Id)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"id": channel.Id}})
}
