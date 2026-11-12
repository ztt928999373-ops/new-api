package service

import (
	"context"
	"errors"
	"slices"
	"time"
)

// CostBucket is an aggregate observation, never evidence for one reservation.
// Amounts are exact USD micro-units supplied by a reviewed read-only adapter.
type CostBucket struct {
	Organization string
	Project      string
	Start, End   int64
	USDmicros    int64
}

type CostPage struct {
	Buckets []CostBucket
	Next    string
}

// ReadOnlyCostSource deliberately exposes neither inference nor write methods.
// This batch has no network implementation and accepts no API credentials.
type ReadOnlyCostSource interface {
	ReadCosts(context.Context, string) (CostPage, error)
}

type CostSnapshot struct {
	Buckets  []CostBucket
	Complete bool
	Alert    string
	// Neither provider enforcement nor request attribution follows from a fetch.
	ProviderHardLimitVerified bool
}

// CollectCostSnapshot stages a bounded snapshot. Partial, stale, conflicting
// or revised input is retained as an anomaly, never applied to billing state.
// previous must represent the same organization/project/window.
func CollectCostSnapshot(ctx context.Context, source ReadOnlyCostSource, organization, project string, start, end int64, now time.Time, previous []CostBucket) (CostSnapshot, error) {
	result := CostSnapshot{}
	if source == nil || organization == "" || project == "" || start < 0 || end <= start || end > now.Unix() || end-start > 31*86400 {
		result.Alert = "invalid_scope"
		return result, errors.New("invalid cost scope")
	}
	// Validate history before indexing it: overwriting a repeated Start would
	// hide a duplicate or conflicting financial observation.
	old := map[int64]CostBucket{}
	if len(previous) > 1000 {
		result.Alert = "previous_bounds"
		return result, nil
	}
	for _, b := range previous {
		if err := ctx.Err(); err != nil {
			result.Alert = "cancelled"
			return result, err
		}
		if b.Organization != organization || b.Project != project || b.Start < start || b.End > end || b.End <= b.Start || b.USDmicros < 0 || b.USDmicros > 9_000_000_000_000_000 {
			result.Alert = "previous_invalid"
			return result, nil
		}
		if prior, exists := old[b.Start]; exists {
			result.Alert = "previous_duplicate"
			if prior != b {
				result.Alert = "previous_conflict"
			}
			return result, nil
		}
		old[b.Start] = b
	}
	if len(previous) > 0 {
		starts := make([]int64, 0, len(old))
		for key := range old {
			starts = append(starts, key)
		}
		slices.Sort(starts)
		position := start
		for _, key := range starts {
			if key < position {
				result.Alert = "previous_overlap"
				return result, nil
			}
			if key > position {
				result.Alert = "previous_missing"
				return result, nil
			}
			position = old[key].End
		}
		if position != end {
			result.Alert = "previous_missing"
			return result, nil
		}
	}
	seen := map[string]bool{}
	buckets := map[int64]CostBucket{}
	cursor := ""
	for range 100 {
		if err := ctx.Err(); err != nil {
			result.Alert = "cancelled"
			return result, err
		}
		if seen[cursor] {
			result.Alert = "pagination_cycle"
			return result, nil
		}
		seen[cursor] = true
		page, err := source.ReadCosts(ctx, cursor)
		if cancellation := ctx.Err(); cancellation != nil {
			result.Alert = "cancelled"
			return result, cancellation
		}
		if err != nil {
			result.Alert = "source_unavailable"
			return result, errors.New("cost source unavailable")
		}
		if len(page.Buckets) > 1000 || len(page.Next) > 1024 {
			result.Alert = "source_bounds"
			return result, nil
		}
		for _, b := range page.Buckets {
			if b.Organization != organization || b.Project != project || b.Start < start || b.End > end || b.End <= b.Start || b.USDmicros < 0 || b.USDmicros > 9_000_000_000_000_000 {
				result.Alert = "scope_or_amount_conflict"
				return result, nil
			}
			if old, ok := buckets[b.Start]; ok {
				if old != b {
					result.Alert = "duplicate_conflict"
					return result, nil
				}
				result.Alert = "duplicate_bucket"
				return result, nil
			}
			if len(buckets) >= 1000 {
				result.Alert = "source_bounds"
				return result, nil
			}
			buckets[b.Start] = b
		}
		if page.Next == "" {
			starts := make([]int64, 0, len(buckets))
			for key := range buckets {
				starts = append(starts, key)
			}
			slices.Sort(starts)
			position := start
			for _, key := range starts {
				if key < position {
					result.Alert = "overlapping_bucket"
					return result, nil
				}
				if key > position {
					result.Alert = "missing_bucket"
					return result, nil
				}
				b := buckets[key]
				result.Buckets = append(result.Buckets, b)
				position = b.End
			}
			if position != end {
				result.Alert = "missing_bucket"
				return result, nil
			}
			if len(previous) > 0 {
				if len(old) != len(buckets) {
					result.Alert = "revision"
					return result, nil
				}
				for key, b := range buckets {
					if old[key] != b {
						result.Alert = "revision"
						return result, nil
					}
				}
			}
			if now.Unix()-end > 48*3600 {
				result.Alert = "stale_snapshot"
				return result, nil
			}
			if err := ctx.Err(); err != nil {
				result.Alert = "cancelled"
				return result, err
			}
			result.Complete = true
			return result, nil
		}
		cursor = page.Next
	}
	result.Alert = "pagination_limit"
	return result, nil
}
