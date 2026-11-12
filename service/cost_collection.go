package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// CollectAndPublishMockCosts has no production credential or arbitrary source
// input. Network staging finishes before publication starts its transaction.
func CollectAndPublishMockCosts(ctx context.Context, db *gorm.DB, source *MockHTTPCostSource, expected int64) (model.AggregateCostVersion, error) {
	if db == nil || source == nil || source.client == nil || expected < 0 || expected >= 9_000_000_000_000_000 {
		return model.AggregateCostVersion{}, errors.New("mock collection rejected")
	}
	snapshot, err := CollectCostSnapshot(ctx, source, source.organization, source.project, source.start, source.end, time.Now(), nil)
	if err != nil || !snapshot.Complete {
		return model.AggregateCostVersion{}, errors.New("mock collection incomplete")
	}
	// Changed complete content appends a review-marked revision. It never
	// overwrites the prior version or participates in request settlement.
	return PublishMockCostSnapshot(ctx, db, source.organization, source.project, source.start, source.end, expected, snapshot)
}

// MockCostRevision exposes observations and bucket-level differences, not a
// payable total or request allocation. Nil sides denote added/removed buckets.
type MockCostRevision struct {
	Version model.AggregateCostVersion
	Changes []MockCostChange
}

type MockCostChange struct {
	Before *CostBucket
	After  *CostBucket
}

// QueryMockCostVersions is an internal read-only service, not an HTTP route.
// Descending revision keyset pagination avoids mutable offset pagination.
// before=0 starts at the newest revision; otherwise the bound is exclusive.
func QueryMockCostVersions(ctx context.Context, db *gorm.DB, organization, project string, start, end, before int64, limit int) ([]MockCostRevision, error) {
	denied := errors.New("mock version query rejected")
	if db == nil || organization == "" || project == "" || start < 0 || end <= start || end-start > 31*86400 || end > time.Now().Unix() || before < 0 || limit < 1 || limit > 100 {
		return nil, denied
	}
	scope := mockCostScope(organization, project, start, end)
	query := db.WithContext(ctx).Where("scope = ?", scope)
	if before > 0 {
		query = query.Where("revision < ?", before)
	}
	// One extra immutable predecessor provides a complete diff for the last
	// returned item. No head is changed and no write transaction is opened.
	var versions []model.AggregateCostVersion
	if query.Order("revision DESC").Limit(limit+1).Find(&versions).Error != nil {
		return nil, denied
	}
	buckets := make([][]CostBucket, len(versions))
	for i, v := range versions {
		if v.Revision <= 0 || len(v.Payload) > 60000 || (i > 0 && versions[i-1].Revision != v.Revision+1) {
			return nil, denied
		}
		digest := sha256.Sum256([]byte(v.Payload))
		if hex.EncodeToString(digest[:]) != v.Digest || common.UnmarshalJsonStr(v.Payload, &buckets[i]) != nil {
			return nil, denied
		}
		// Historical reads validate scope/coverage without treating age as a
		// reason to erase history. The original end is the validation clock.
		checked, err := CollectCostSnapshot(ctx, stagedMockCosts{buckets[i]}, organization, project, start, end, time.Unix(end, 0), nil)
		if err != nil || !checked.Complete {
			return nil, denied
		}
		canonical, err := common.Marshal(checked.Buckets)
		if err != nil || string(canonical) != v.Payload {
			return nil, denied
		}
	}
	result := make([]MockCostRevision, 0, min(limit, len(versions)))
	for i := 0; i < min(limit, len(versions)); i++ {
		row := MockCostRevision{Version: versions[i]}
		var previous []CostBucket
		if versions[i].Revision > 1 {
			if i+1 >= len(versions) {
				return nil, denied // a missing predecessor cannot yield a false diff
			}
			previous = buckets[i+1]
		}
		// Both lists were canonicalized in ascending Start order. Merge them
		// without adding amounts or deriving any per-request financial fact.
		for a, b := 0, 0; a < len(previous) || b < len(buckets[i]); {
			var change MockCostChange
			switch {
			case b == len(buckets[i]) || (a < len(previous) && previous[a].Start < buckets[i][b].Start):
				change.Before = &previous[a]
				a++
			case a == len(previous) || buckets[i][b].Start < previous[a].Start:
				change.After = &buckets[i][b]
				b++
			default:
				if previous[a] != buckets[i][b] {
					change.Before, change.After = &previous[a], &buckets[i][b]
				}
				a++
				b++
			}
			if change.Before != nil || change.After != nil {
				row.Changes = append(row.Changes, change)
			}
		}
		result = append(result, row)
	}
	return result, nil
}
