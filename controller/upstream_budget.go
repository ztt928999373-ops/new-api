package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func GetUpstreamBudget(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(400, gin.H{"success": false, "code": "UPSTREAM_BUDGET_INVALID"})
		return
	}
	var p model.UpstreamBudget
	if model.DB.First(&p, "channel_id = ?", id).Error != nil {
		c.JSON(404, gin.H{"success": false, "code": "UPSTREAM_BUDGET_NOT_CONFIGURED"})
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
	query := model.DB.Model(&model.UpstreamCostReservation{}).Where("channel_id = ?", id)
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
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(400, gin.H{"success": false, "code": "UPSTREAM_BUDGET_INVALID"})
		return
	}
	var input struct {
		ReservationID string `json:"reservation_id"`
		ActualMicros  int64  `json:"actual_micros"`
		Evidence      string `json:"evidence"`
	}
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(400, gin.H{"success": false, "code": "UPSTREAM_BUDGET_INVALID"})
		return
	}
	var r model.UpstreamCostReservation
	if model.DB.First(&r, "id = ? AND channel_id = ?", input.ReservationID, id).Error != nil || model.ReconcileUpstreamCost(c.Request.Context(), model.DB, r.ID, input.ActualMicros, input.Evidence) != nil {
		c.JSON(409, gin.H{"success": false, "code": "UPSTREAM_BUDGET_RECONCILIATION_REJECTED"})
		return
	}
	recordManageAudit(c, "upstream_budget.reconcile", map[string]any{"channel_id": id, "reservation_id": r.ID, "actual_micros": input.ActualMicros})
	c.JSON(200, gin.H{"success": true})
}
