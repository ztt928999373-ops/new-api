package model

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"math"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrUpstreamBudget = errors.New("upstream budget unavailable")

// All amounts are USD micro-units, independent of wallet quota. Charged is
// conservatively allocated liability, NOT a measured supplier invoice.
const MaxUpstreamBudgetMicros int64 = 9_000_000_000_000_000

type UpstreamBudget struct {
	ChannelID            int    `gorm:"primaryKey;autoIncrement:false" json:"channel_id"`
	Enabled              bool   `json:"enabled"`
	Revision             int64  `json:"revision"`
	LimitMicros          int64  `json:"limit_micros"`
	HeldMicros           int64  `json:"held_micros"`
	ChargedMicros        int64  `json:"allocated_liability_micros"`
	AttemptCeilingMicros int64  `json:"attempt_ceiling_micros"`
	Model                string `gorm:"size:128" json:"model"`
	MaxRequestBytes      int64  `json:"max_request_bytes"`
	MaxOutputTokens      int64  `json:"max_output_tokens"`
	ValidUntil           int64  `json:"valid_until"`
	CeilingConfirmed     bool   `json:"ceiling_confirmed"`
	CostBasisHash        string `gorm:"size:64" json:"cost_basis_hash"`
	ReconciliationFrozen bool   `json:"reconciliation_frozen"`
}

type UpstreamCostReservation struct {
	ID             string `gorm:"primaryKey;size:64" json:"id"`
	ChannelID      int    `gorm:"index" json:"channel_id"`
	PolicyRevision int64  `json:"policy_revision"`
	CeilingMicros  int64  `json:"ceiling_micros"`
	SettledMicros  int64  `json:"settled_micros"`
	State          string `gorm:"size:32;index" json:"state"`
	CreatedAt      int64  `json:"created_at"`
	EvidenceHash   string `gorm:"size:64" json:"evidence_hash,omitempty"`
	RequestID      string `gorm:"size:64;index" json:"request_id"`
}

func ConfigureUpstreamBudget(ctx context.Context, db *gorm.DB, policy UpstreamBudget, expected int64) error {
	now := time.Now().Unix()
	if policy.Enabled && (!policy.CeilingConfirmed || len(policy.CostBasisHash) != 64) {
		return ErrUpstreamBudget
	}
	if policy.ChannelID <= 0 || expected < 0 || expected >= MaxUpstreamBudgetMicros || policy.LimitMicros < 0 || policy.LimitMicros > MaxUpstreamBudgetMicros || policy.AttemptCeilingMicros <= 0 || policy.AttemptCeilingMicros > policy.LimitMicros || policy.Model == "" || len(policy.Model) > 128 || policy.MaxRequestBytes < 1 || policy.MaxRequestBytes > 1<<20 || policy.MaxOutputTokens < 1 || policy.MaxOutputTokens > 32768 || policy.ValidUntil <= now || policy.ValidUntil > now+30*86400 {
		return ErrUpstreamBudget
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if tx.Select("id", "status", "upstream_budget_enabled").Where("id = ? AND type = ? AND credential_ref <> ?", policy.ChannelID, constant.ChannelTypeOpenAI, "").First(&channel).Error != nil {
			return ErrUpstreamBudget
		}
		// Initial activation requires maintenance. Operators must drain calls
		// admitted under the previous unprotected policy before enabling.
		if policy.Enabled && !channel.UpstreamBudgetEnabled && channel.Status == common.ChannelStatusEnabled {
			return ErrUpstreamBudget
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&UpstreamBudget{ChannelID: policy.ChannelID}).Error; err != nil {
			return ErrUpstreamBudget
		}
		query := tx.Model(&UpstreamBudget{}).Where("channel_id = ? AND revision = ? AND held_micros <= ? - charged_micros", policy.ChannelID, expected, policy.LimitMicros)
		if policy.Enabled {
			query = query.Where("(reconciliation_frozen = ? OR reconciliation_frozen IS NULL)", false)
		}
		result := query.Updates(map[string]any{
			"enabled": policy.Enabled, "revision": expected + 1, "limit_micros": policy.LimitMicros,
			"attempt_ceiling_micros": policy.AttemptCeilingMicros, "model": policy.Model,
			"max_request_bytes": policy.MaxRequestBytes, "max_output_tokens": policy.MaxOutputTokens, "valid_until": policy.ValidUntil,
			"ceiling_confirmed": policy.CeilingConfirmed, "cost_basis_hash": policy.CostBasisHash,
		})
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrUpstreamBudget
		}
		// Configuration enrolls the channel permanently in this pilot. Disabled
		// means frozen, never an opt-out; no exit API is offered. Preserve this
		// marker through restore and all future configuration changes.
		result = tx.Model(&Channel{}).Where("id = ? AND credential_ref <> ?", policy.ChannelID, "").Update("upstream_budget_enabled", true)
		if result.Error != nil {
			return ErrUpstreamBudget
		}
		return nil
	})
}

func ReserveUpstreamCost(ctx context.Context, db *gorm.DB, policy UpstreamBudget) (UpstreamCostReservation, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return UpstreamCostReservation{}, ErrUpstreamBudget
	}
	amount := policy.AttemptCeilingMicros
	if amount <= 0 || amount > MaxUpstreamBudgetMicros {
		return UpstreamCostReservation{}, ErrUpstreamBudget
	}
	r := UpstreamCostReservation{ID: hex.EncodeToString(id[:]), ChannelID: policy.ChannelID, PolicyRevision: policy.Revision, CeilingMicros: amount, State: "held", CreatedAt: time.Now().Unix()}
	r.RequestID, _ = ctx.Value(common.RequestIdKey).(string)
	if r.RequestID == "" || len(r.RequestID) > 64 {
		r.RequestID = common.NewRequestId()
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&UpstreamBudget{}).Where("channel_id = ? AND enabled = ? AND (reconciliation_frozen = ? OR reconciliation_frozen IS NULL) AND revision = ? AND attempt_ceiling_micros = ? AND ceiling_confirmed = ? AND valid_until > ? AND held_micros <= limit_micros - charged_micros - ?", policy.ChannelID, true, false, policy.Revision, amount, true, time.Now().Unix(), amount).Update("held_micros", gorm.Expr("held_micros + ?", amount))
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrUpstreamBudget
		}
		if err := tx.Create(&r).Error; err != nil {
			return ErrUpstreamBudget
		}
		return nil
	})
	if err != nil {
		return UpstreamCostReservation{}, ErrUpstreamBudget
	}
	return r, nil
}

// Charging never reduces total exposure. A crash or failed write keeps held
// liability durable. There is deliberately no TTL-based automatic release.
func ChargeUpstreamCeiling(ctx context.Context, db *gorm.DB, id string) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var r UpstreamCostReservation
		if err := tx.First(&r, "id = ?", id).Error; err != nil {
			return ErrUpstreamBudget
		}
		if r.State != "held" {
			return nil
		}
		update := tx.Model(&UpstreamCostReservation{}).Where("id = ? AND state = ?", id, "held").Updates(map[string]any{"state": "ceiling_charged", "settled_micros": r.CeilingMicros})
		if update.Error != nil {
			return ErrUpstreamBudget
		}
		if update.RowsAffected == 0 {
			return nil
		}
		update = tx.Model(&UpstreamBudget{}).Where("channel_id = ? AND held_micros >= ? AND charged_micros <= ?", r.ChannelID, r.CeilingMicros, int64(math.MaxInt64)-r.CeilingMicros).Updates(map[string]any{"held_micros": gorm.Expr("held_micros - ?", r.CeilingMicros), "charged_micros": gorm.Expr("charged_micros + ?", r.CeilingMicros)})
		if update.Error != nil || update.RowsAffected != 1 {
			return ErrUpstreamBudget
		}
		return nil
	})
}

// Reconciliation is an explicit trusted-operator assertion of supplier cost,
// not inference from wallet refunds, HTTP status or locally estimated usage.
func ReconcileUpstreamCost(ctx context.Context, db *gorm.DB, id string, actual int64, evidence string) error {
	if actual < 0 || actual > MaxUpstreamBudgetMicros || len(evidence) < 8 || len(evidence) > 256 {
		return ErrUpstreamBudget
	}
	h := sha256.Sum256([]byte(evidence))
	hash := hex.EncodeToString(h[:])
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var r UpstreamCostReservation
		if err := tx.First(&r, "id = ?", id).Error; err != nil {
			return ErrUpstreamBudget
		}
		if r.State == "reconciled" || r.State == "reconciled_over_ceiling" {
			if r.SettledMicros == actual && r.EvidenceHash == hash {
				return nil
			}
			return ErrUpstreamBudget
		}
		state := "reconciled"
		if actual > r.CeilingMicros {
			state = "reconciled_over_ceiling"
		}
		update := tx.Model(&UpstreamCostReservation{}).Where("id = ? AND state = ?", id, r.State).Updates(map[string]any{"state": state, "settled_micros": actual, "evidence_hash": hash})
		if update.Error != nil || update.RowsAffected != 1 {
			return ErrUpstreamBudget
		}
		values := map[string]any{}
		if actual > r.CeilingMicros {
			values["enabled"] = false
		} // channel marker remains true: frozen, NOT opted out
		if r.State == "held" {
			values["held_micros"] = gorm.Expr("held_micros - ?", r.CeilingMicros)
			values["charged_micros"] = gorm.Expr("charged_micros + ?", actual)
		} else if r.State == "ceiling_charged" {
			values["charged_micros"] = gorm.Expr("charged_micros - ?", r.CeilingMicros-actual)
		} else {
			return ErrUpstreamBudget
		}
		query := tx.Model(&UpstreamBudget{}).Where("channel_id = ? AND charged_micros <= ?", r.ChannelID, int64(math.MaxInt64)-actual)
		if r.State == "held" {
			query = query.Where("held_micros >= ?", r.CeilingMicros)
		} else {
			query = query.Where("charged_micros >= ?", r.CeilingMicros)
		}
		update = query.Updates(values)
		if update.Error != nil || update.RowsAffected != 1 {
			return ErrUpstreamBudget
		}
		return nil
	})
}
