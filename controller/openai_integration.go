// Copyright (c) QuantumNous and contributors.
package controller

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// PrepareOpenAIIntegration creates a disabled, single-model pilot channel through
// the ordinary channel creation and audit path. Credentials are write-only here.
// The existing privileged key-view endpoint remains unchanged.
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
	payload, err := common.Marshal(AddChannelRequest{Mode: "single", Channel: &model.Channel{
		Type: constant.ChannelTypeOpenAI, Name: input.Name, Key: input.Key,
		Models: input.Model, Group: "openai-pilot", BaseURL: &baseURL,
		Status: common.ChannelStatusManuallyDisabled,
	}})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Invalid request"})
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(payload))
	c.Request.ContentLength = int64(len(payload))
	AddChannel(c)
}
