package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func GetUpstreamBudget(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(400, gin.H{"success": false, "code": "UPSTREAM_BUDGET_INVALID"})
		return
	}
	var p model.UpstreamBudget
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	err = model.DB.WithContext(ctx).First(&p, "channel_id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(404, gin.H{"success": false, "code": "UPSTREAM_BUDGET_NOT_CONFIGURED"})
		return
	}
	if err != nil {
		c.JSON(503, gin.H{"success": false, "code": "UPSTREAM_BUDGET_UNAVAILABLE"})
		return
	}
	var reservations []model.UpstreamCostReservation
	page := 1
	if raw := c.Query("page"); raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 || page > 10000 {
			c.JSON(400, gin.H{"success": false, "code": "UPSTREAM_BUDGET_INVALID"})
			return
		}
	}
	query := model.DB.WithContext(ctx).Model(&model.UpstreamCostReservation{}).Where("channel_id = ?", id)
	if state := c.Query("state"); state != "" {
		switch state {
		case "held", "ceiling_charged", "reconciled", "reconciled_over_ceiling":
			query = query.Where("state = ?", state)
		default:
			c.JSON(400, gin.H{"success": false, "code": "UPSTREAM_BUDGET_INVALID"})
			return
		}
	}
	var total int64
	if query.Count(&total).Error != nil || query.Order("created_at DESC").Order("id DESC").Offset((page-1)*50).Limit(50).Find(&reservations).Error != nil {
		c.JSON(503, gin.H{"success": false, "code": "UPSTREAM_BUDGET_UNAVAILABLE"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": p, "reservations": reservations, "total": total, "page": page, "accounting": "conservative_ceiling_not_supplier_invoice"})
}

func ConfigureUpstreamBudget(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(400, gin.H{"success": false, "code": "UPSTREAM_BUDGET_INVALID"})
		return
	}
	var input struct {
		model.UpstreamBudget
		ExpectedRevision int64  `json:"expected_revision"`
		CostBasis        string `json:"cost_basis"`
	}
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(400, gin.H{"success": false, "code": "UPSTREAM_BUDGET_INVALID"})
		return
	}
	var ch model.Channel
	if model.DB.First(&ch, id).Error != nil || ch.Type != constant.ChannelTypeOpenAI || ch.CredentialRef == "" || ch.Key != "" {
		c.JSON(400, gin.H{"success": false, "code": "UPSTREAM_BUDGET_UNSUPPORTED"})
		return
	}
	input.ChannelID = id
	if input.Enabled {
		if !input.CeilingConfirmed || len(input.CostBasis) < 16 || len(input.CostBasis) > 2048 {
			c.JSON(400, gin.H{"success": false, "code": "UPSTREAM_COST_BOUND_CONFIRMATION_REQUIRED"})
			return
		}
		h := sha256.Sum256([]byte(input.CostBasis))
		input.CostBasisHash = hex.EncodeToString(h[:])
	}
	if model.ConfigureUpstreamBudget(c.Request.Context(), model.DB, input.UpstreamBudget, input.ExpectedRevision) != nil {
		c.JSON(409, gin.H{"success": false, "code": "UPSTREAM_BUDGET_CONFLICT_OR_INVALID"})
		return
	}
	recordManageAudit(c, "upstream_budget.configure", map[string]any{"channel_id": id, "enabled": input.Enabled, "protection": "retained", "revision": input.ExpectedRevision + 1, "limit_micros": input.LimitMicros, "attempt_ceiling_micros": input.AttemptCeilingMicros, "cost_basis_hash": input.CostBasisHash, "valid_until": input.ValidUntil})
	c.JSON(200, gin.H{"success": true, "protection": "retained", "budget_enabled": input.Enabled})
}

func ReconcileUpstreamBudget(c *gin.Context) {
	// The legacy payload cannot establish a final structured supplier bill.
	// Never fabricate a statement or mutate historical liabilities from it.
	// Keep the authenticated route so clients get an explicit migration error.
	c.JSON(http.StatusGone, gin.H{"success": false, "code": "SUPPLIER_BILL_REQUIRED", "replacement": "/api/channel/:id/upstream-budget/statements"})
	params := map[string]any{"reason": "structured_supplier_bill_required"}
	if id, err := strconv.Atoi(c.Param("id")); err == nil && id > 0 {
		params["channel_id"] = id
	}
	info := &model.AuditRequestInfo{Method: c.Request.Method, Route: c.FullPath(), Path: c.FullPath(), Status: http.StatusGone, Success: false}
	model.RecordOperationAuditLog(c.GetInt("id"), c.GetInt("role"), auditContentEN("upstream_budget.reconcile", params), c.ClientIP(), "upstream_budget.reconcile", params, auditOperatorInfo(c), info, c)
	markAuditLogged(c)
}
