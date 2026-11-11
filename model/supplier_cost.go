package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrSupplierCost = errors.New("supplier bill invalid or unavailable")
var ErrSupplierCostConflict = errors.New("supplier bill conflicts with recorded evidence")

// SupplierCostStatement is a trusted operator's bill observation, NOT an
// independently fetched invoice. Raw account/bill/evidence text is never stored.
type SupplierCostStatement struct {
	ID                   string  `gorm:"primaryKey;size:64" json:"id"`
	ChannelID            int     `gorm:"index" json:"channel_id"`
	ReservationID        string  `gorm:"size:64;index" json:"reservation_id"`
	SettledReservationID *string `gorm:"size:64;uniqueIndex" json:"-"`
	AccountHash          string  `gorm:"size:64" json:"account_hash"`
	InputHash            string  `gorm:"size:64" json:"input_hash"`
	EvidenceHash         string  `gorm:"size:64" json:"evidence_hash"`
	LastConflictHash     string  `gorm:"size:64" json:"last_conflict_hash,omitempty"`
	ActualMicros         int64   `json:"observed_supplier_cost_usd_micros"`
	EstimatedMicros      *int64  `json:"operator_estimated_cost_usd_micros"`
	DeltaMicros          *int64  `json:"actual_minus_estimate_usd_micros"`
	CeilingMicros        int64   `json:"reserved_ceiling_usd_micros"`
	State                string  `gorm:"size:32" json:"state"`
	Alert                string  `gorm:"size:64;index" json:"alert"`
	RequestID            string  `gorm:"size:64;index" json:"request_id"`
	CreatedAt            int64   `json:"created_at"`
}

type SupplierCostInput struct {
	Account         string `json:"account"`
	Bill            string `json:"bill"`
	Line            string `json:"line"`
	Currency        string `json:"currency"`
	ReservationID   string `json:"reservation_id"`
	ActualMicros    int64  `json:"actual_micros"`
	EstimatedMicros *int64 `json:"estimated_micros"`
	Evidence        string `json:"evidence"`
	Confirmed       bool   `json:"confirmed_final_bill"`
}

// ImportSupplierCost reuses the existing liability settlement transaction.
// Anomalies latch a separate durable gate; neither configuration nor retries
// can clear it. No automatic thaw, credit, wallet change, or external I/O.
func ImportSupplierCost(ctx context.Context, db *gorm.DB, channelID int, input SupplierCostInput) (SupplierCostStatement, error) {
	if channelID < 1 || !input.Confirmed || input.Currency != "USD" || len(input.Account) < 1 || len(input.Account) > 128 || len(input.Bill) < 1 || len(input.Bill) > 128 || len(input.Line) < 1 || len(input.Line) > 128 || len(input.Evidence) < 16 || len(input.Evidence) > 256 || input.ActualMicros < 0 || input.ActualMicros > MaxUpstreamBudgetMicros || (input.EstimatedMicros != nil && (*input.EstimatedMicros < 0 || *input.EstimatedMicros > MaxUpstreamBudgetMicros)) {
		return SupplierCostStatement{}, ErrSupplierCost
	}
	idBytes, err := hex.DecodeString(input.ReservationID)
	if err != nil || len(idBytes) != 16 || hex.EncodeToString(idBytes) != input.ReservationID {
		return SupplierCostStatement{}, ErrSupplierCost
	}
	identity, _ := common.Marshal([]string{"openai", input.Account, input.Bill, input.Line, input.Currency})
	idHash := sha256.Sum256(identity)
	payload, _ := common.Marshal(input)
	payloadHash := sha256.Sum256(payload)
	accountHash := sha256.Sum256([]byte(input.Account))
	evidenceHash := sha256.Sum256([]byte(input.Evidence))
	row := SupplierCostStatement{ID: hex.EncodeToString(idHash[:]), ChannelID: channelID, ReservationID: input.ReservationID, AccountHash: hex.EncodeToString(accountHash[:]), InputHash: hex.EncodeToString(payloadHash[:]), EvidenceHash: hex.EncodeToString(evidenceHash[:]), ActualMicros: input.ActualMicros, EstimatedMicros: input.EstimatedMicros, State: "pending", CreatedAt: time.Now().Unix()}
	conflict := false
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var policy UpstreamBudget
		if lockForUpdate(tx).First(&policy, "channel_id = ?", channelID).Error != nil {
			return ErrSupplierCost
		}
		created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if created.Error != nil {
			return ErrSupplierCost
		}
		if created.RowsAffected == 0 {
			var previous SupplierCostStatement
			if tx.First(&previous, "id = ?", row.ID).Error != nil {
				return ErrSupplierCost
			}
			if previous.ChannelID != channelID || previous.InputHash != row.InputHash {
				conflict = true
				if previous.ChannelID == channelID {
					if tx.Model(&previous).Updates(map[string]any{"alert": "conflicting_bill", "last_conflict_hash": row.InputHash}).Error != nil {
						return ErrSupplierCost
					}
					previous.Alert, previous.LastConflictHash = "conflicting_bill", row.InputHash
				}
				row = previous
				return freezeSupplierCostReview(tx, channelID)
			}
			row = previous
			if row.State != "in_flight" {
				return nil
			}
		}
		var reservation UpstreamCostReservation
		lookup := tx.Where("id = ? AND channel_id = ?", input.ReservationID, channelID).First(&reservation).Error
		if lookup != nil && !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return ErrSupplierCost
		}
		switch {
		case lookup != nil:
			row.State, row.Alert = "unmatched", "reservation_not_found"
		case reservation.State == "held":
			row.State, row.Alert = "in_flight", "attempt_not_finished"
		default:
			row.RequestID, row.CeilingMicros = reservation.RequestID, reservation.CeilingMicros
			var count int64
			if tx.Model(&SupplierCostStatement{}).Where("settled_reservation_id = ? AND id <> ?", reservation.ID, row.ID).Count(&count).Error != nil {
				return ErrSupplierCost
			}
			if count != 0 {
				row.State, row.Alert = "unmatched", "duplicate_reservation"
			} else if ReconcileUpstreamCost(ctx, tx, reservation.ID, input.ActualMicros, input.Evidence) != nil {
				row.State, row.Alert = "unmatched", "settlement_conflict"
			} else {
				row.State, row.Alert, row.SettledReservationID = "settled", "", &reservation.ID
				if input.EstimatedMicros != nil {
					delta := input.ActualMicros - *input.EstimatedMicros
					row.DeltaMicros = &delta
				}
				switch {
				case input.ActualMicros > reservation.CeilingMicros:
					row.Alert = "above_reserved_ceiling"
				case input.EstimatedMicros == nil:
					row.Alert = "estimate_missing"
				case *row.DeltaMicros > 0:
					row.Alert = "above_estimate"
				}
			}
		}
		if row.LastConflictHash != "" {
			row.Alert = "conflicting_bill"
		}
		if tx.Save(&row).Error != nil {
			return ErrSupplierCost
		}
		if row.Alert != "" {
			return freezeSupplierCostReview(tx, channelID)
		}
		return nil
	})
	if err != nil {
		return SupplierCostStatement{}, ErrSupplierCost
	}
	if conflict {
		return row, ErrSupplierCostConflict
	}
	return row, nil
}

func freezeSupplierCostReview(tx *gorm.DB, channelID int) error {
	if tx.Model(&UpstreamBudget{}).Where("channel_id = ?", channelID).Updates(map[string]any{"enabled": false, "reconciliation_frozen": true}).Error != nil {
		return ErrSupplierCost
	}
	return tx.Model(&Channel{}).Where("id = ?", channelID).Update("upstream_budget_enabled", true).Error
}
