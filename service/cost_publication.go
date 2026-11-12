package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type stagedMockCosts struct{ buckets []CostBucket }

func (s stagedMockCosts) ReadCosts(context.Context, string) (CostPage, error) {
	return CostPage{Buckets: s.buckets}, nil
}

// PublishMockCostSnapshot atomically publishes only revalidated complete Mock
// observations. It never attributes costs to requests or changes spend state.
// A changed complete snapshot is retained as a new revision requiring review.
func PublishMockCostSnapshot(ctx context.Context, db *gorm.DB, organization, project string, start, end, expected int64, snapshot CostSnapshot) (model.AggregateCostVersion, error) {
	var result model.AggregateCostVersion
	denied := errors.New("aggregate publication rejected")
	if db == nil || expected < 0 || expected >= 9_000_000_000_000_000 || !snapshot.Complete || snapshot.Alert != "" || snapshot.ProviderHardLimitVerified {
		return result, denied
	}
	checked, err := CollectCostSnapshot(ctx, stagedMockCosts{snapshot.Buckets}, organization, project, start, end, time.Now(), nil)
	if err != nil || !checked.Complete {
		return result, denied
	}
	payload, err := common.Marshal(checked.Buckets)
	if err != nil || len(payload) > 60000 {
		return result, denied
	}
	scopeBytes, _ := common.Marshal([]any{"mock-openai-costs", organization, project, start, end})
	scopeHash := sha256.Sum256(scopeBytes)
	digest := sha256.Sum256(payload)
	scope, digestText := hex.EncodeToString(scopeHash[:]), hex.EncodeToString(digest[:])
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.AggregateCostHead{Scope: scope}).Error != nil {
			return denied
		}
		var head model.AggregateCostHead
		if tx.First(&head, "scope = ?", scope).Error != nil {
			return denied
		}
		if head.Revision > 0 && head.Digest == digestText {
			return tx.First(&result, "scope = ? AND revision = ?", scope, head.Revision).Error
		}
		update := tx.Model(&model.AggregateCostHead{}).Where("scope = ? AND revision = ?", scope, expected).Updates(map[string]any{"revision": expected + 1, "digest": digestText})
		if update.Error != nil || update.RowsAffected != 1 {
			return denied
		}
		id := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", scope, expected+1)))
		state := "initial_mock_observation"
		if expected > 0 {
			state = "revision_requires_review"
		}
		result = model.AggregateCostVersion{ID: hex.EncodeToString(id[:]), Scope: scope, Revision: expected + 1, Digest: digestText, Payload: string(payload), State: state, CreatedAt: time.Now().Unix()}
		return tx.Create(&result).Error
	})
	if err != nil {
		return model.AggregateCostVersion{}, denied
	}
	return result, nil
}
