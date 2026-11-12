package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func ImportSupplierCost(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(400, gin.H{"success": false, "code": "SUPPLIER_BILL_INVALID"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var input struct {
		model.SupplierCostInput
		ActualMicros *int64 `json:"actual_micros"`
	}
	if c.ShouldBindJSON(&input) != nil || input.ActualMicros == nil {
		c.JSON(400, gin.H{"success": false, "code": "SUPPLIER_BILL_INVALID"})
		return
	}
	input.SupplierCostInput.ActualMicros = *input.ActualMicros
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	row, err := model.ImportSupplierCost(ctx, model.DB, id, input.SupplierCostInput)
	if err != nil && !errors.Is(err, model.ErrSupplierCostConflict) {
		c.JSON(409, gin.H{"success": false, "code": "SUPPLIER_BILL_INVALID_OR_UNAVAILABLE"})
		return
	}
	if err != nil {
		c.JSON(409, gin.H{"success": false, "code": "SUPPLIER_BILL_CONFLICT"})
	} else {
		c.JSON(200, gin.H{"success": true, "data": row, "verification": "operator_attested_not_independently_verified"})
	}
	// Capture the final status, including rejected conflicts. Raw evidence,
	// account and bill IDs never enter the generic operation audit.
	params := map[string]any{"channel_id": id, "statement_id": row.ID, "state": row.State, "alert": row.Alert, "conflict": err != nil}
	info := &model.AuditRequestInfo{Method: c.Request.Method, Route: c.FullPath(), Path: c.FullPath(), Status: c.Writer.Status(), Success: err == nil}
	model.RecordOperationAuditLog(c.GetInt("id"), c.GetInt("role"), auditContentEN("upstream_budget.bill_review", params), c.ClientIP(), "upstream_budget.bill_review", params, auditOperatorInfo(c), info, c)
	markAuditLogged(c)
}

func ListSupplierCosts(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	page, pageErr := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || id < 1 || pageErr != nil || page < 1 || page > 10000 {
		c.JSON(400, gin.H{"success": false, "code": "SUPPLIER_BILL_INVALID"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	query := model.DB.WithContext(ctx).Where("channel_id = ?", id)
	if state := c.Query("state"); state != "" {
		switch state {
		case "pending", "in_flight", "unmatched", "settled":
			query = query.Where("state = ?", state)
		default:
			c.JSON(400, gin.H{"success": false, "code": "SUPPLIER_BILL_INVALID"})
			return
		}
	}
	var start, end int64
	for _, boundary := range []struct {
		name  string
		value *int64
	}{{"start", &start}, {"end", &end}} {
		if raw := c.Query(boundary.name); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value < 0 || value > 253402300799 {
				c.JSON(400, gin.H{"success": false, "code": "SUPPLIER_BILL_INVALID"})
				return
			}
			*boundary.value = value
		}
	}
	if c.Query("end") != "" && c.Query("start") != "" && start >= end {
		c.JSON(400, gin.H{"success": false, "code": "SUPPLIER_BILL_INVALID"})
		return
	}
	if c.Query("start") != "" {
		query = query.Where("created_at >= ?", start)
	}
	if c.Query("end") != "" {
		query = query.Where("created_at < ?", end)
	}
	if c.Query("alerts_only") == "true" {
		query = query.Where("alert <> ?", "")
	}
	var rows []model.SupplierCostStatement
	var total int64
	if query.Model(&model.SupplierCostStatement{}).Count(&total).Error != nil || query.Order("created_at DESC").Order("id DESC").Offset((page-1)*50).Limit(50).Find(&rows).Error != nil {
		c.JSON(503, gin.H{"success": false, "code": "SUPPLIER_BILL_UNAVAILABLE"})
		return
	}
	// A request can have multiple paid retries but one user charge. Return the
	// existing log references separately, once, without per-attempt allocation.
	requestIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.RequestID != "" {
			requestIDs = append(requestIDs, row.RequestID)
		}
	}
	type chargeReference struct {
		ID        int    `json:"log_id"`
		RequestID string `json:"request_id"`
		Quota     int    `json:"quota"`
		Type      int    `json:"log_type"`
	}
	var charges []chargeReference
	chargeStatus := "available_references_not_totals"
	if len(requestIDs) > 0 {
		if model.LOG_DB == nil || model.LOG_DB.WithContext(ctx).Model(&model.Log{}).Select("id", "request_id", "quota", "type").Where("channel_id = ? AND request_id IN ? AND type IN ?", id, requestIDs, []int{model.LogTypeConsume, model.LogTypeRefund}).Order("created_at").Limit(501).Scan(&charges).Error != nil {
			charges, chargeStatus = nil, "unavailable"
		} else if len(charges) > 500 {
			charges, chargeStatus = charges[:500], "truncated_references_not_totals"
		}
	}
	c.JSON(200, gin.H{"success": true, "data": rows, "page": page, "page_size": 50, "total": total, "time_basis": "statement_recorded_at_not_supplier_usage_period", "user_charge_references": charges, "user_charge_unit": "quota", "user_charge_status": chargeStatus,
		"platform_revenue_usd_micros": nil, "gross_profit_usd_micros": nil,
		"revenue_status": "unknown_without_revenue_allocation", "verification": "operator_attested_not_independently_verified", "supplier_account_hard_limit": "not_connected"})
}
