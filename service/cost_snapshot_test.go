package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type mockCostSource struct {
	pages   map[string]CostPage
	failure bool
}

func (m mockCostSource) ReadCosts(_ context.Context, cursor string) (CostPage, error) {
	if m.failure {
		return CostPage{}, errors.New("synthetic failure")
	}
	return m.pages[cursor], nil
}

func TestReadOnlyCostSnapshot(t *testing.T) {
	now := time.Unix(200000, 0)
	bucket := CostBucket{Organization: "synthetic-org", Project: "synthetic-project", Start: 190000, End: 195000, USDmicros: 7}
	for _, scenario := range []string{"complete", "duplicate", "conflict", "missing", "scope", "negative", "cycle", "failure", "revision", "cancelled", "stale"} {
		t.Run(scenario, func(t *testing.T) {
			source := mockCostSource{pages: map[string]CostPage{"": {Buckets: []CostBucket{bucket}}}}
			var previous []CostBucket
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "duplicate":
				source.pages[""] = CostPage{Buckets: []CostBucket{bucket, bucket}}
			case "conflict":
				other := bucket
				other.USDmicros = 8
				source.pages[""] = CostPage{Buckets: []CostBucket{bucket, other}}
			case "missing":
				source.pages[""] = CostPage{}
			case "scope":
				other := bucket
				other.Project = "other"
				source.pages[""] = CostPage{Buckets: []CostBucket{other}}
			case "negative":
				other := bucket
				other.USDmicros = -1
				source.pages[""] = CostPage{Buckets: []CostBucket{other}}
			case "cycle":
				source.pages[""] = CostPage{Next: "repeat"}
				source.pages["repeat"] = CostPage{Next: "repeat"}
			case "failure":
				source.failure = true
			case "revision":
				old := bucket
				old.USDmicros = 6
				previous = []CostBucket{old}
			case "cancelled":
				cancel()
			case "stale":
				now = time.Unix(400000, 0)
				defer func() { now = time.Unix(200000, 0) }()
			}
			result, err := CollectCostSnapshot(ctx, source, bucket.Organization, bucket.Project, bucket.Start, bucket.End, now, previous)
			if scenario == "failure" || scenario == "cancelled" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.False(t, result.ProviderHardLimitVerified)
			if scenario == "complete" {
				require.True(t, result.Complete)
				require.Len(t, result.Buckets, 1)
			} else {
				require.False(t, result.Complete)
			}
		})
	}
}

type cancellingCostSource struct {
	page   CostPage
	cancel context.CancelFunc
}

func (s cancellingCostSource) ReadCosts(context.Context, string) (CostPage, error) {
	s.cancel()
	return s.page, nil
}

func TestReadOnlyCostSnapshotBoundaries(t *testing.T) {
	b := CostBucket{Organization: "org", Project: "project", Start: 100, End: 200, USDmicros: 1}
	for _, name := range []string{"cross_page_duplicate", "previous_duplicate", "previous_conflict", "previous_scope", "previous_overlap", "previous_crossing_overlap", "previous_gap", "previous_negative", "overlap", "crossing_overlap", "page_limit", "cancel_after_read", "valid_previous"} {
		t.Run(name, func(t *testing.T) {
			mock := mockCostSource{pages: map[string]CostPage{"": {Buckets: []CostBucket{b}}}}
			var source ReadOnlyCostSource
			var previous []CostBucket
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			expected := ""
			switch name {
			case "cross_page_duplicate":
				mock.pages[""] = CostPage{Buckets: []CostBucket{b}, Next: "two"}
				mock.pages["two"] = CostPage{Buckets: []CostBucket{b}}
				expected = "duplicate_bucket"
			case "previous_duplicate":
				previous = []CostBucket{b, b}
				expected = "previous_duplicate"
			case "previous_conflict":
				other := b
				other.USDmicros = 2
				previous = []CostBucket{other, b}
				expected = "previous_conflict"
			case "previous_scope":
				other := b
				other.Project = "other"
				previous = []CostBucket{other}
				expected = "previous_invalid"
			case "previous_overlap":
				other := b
				other.Start = 150
				previous = []CostBucket{b, other}
				expected = "previous_overlap"
			case "previous_crossing_overlap", "crossing_overlap":
				first, second := b, b
				first.End, second.Start = 160, 150
				if name == "previous_crossing_overlap" {
					previous = []CostBucket{first, second}
					expected = "previous_overlap"
				} else {
					mock.pages[""] = CostPage{Buckets: []CostBucket{first, second}}
					expected = "overlapping_bucket"
				}
			case "previous_gap":
				other := b
				other.Start = 110
				previous = []CostBucket{other}
				expected = "previous_missing"
			case "previous_negative":
				other := b
				other.USDmicros = -1
				previous = []CostBucket{other}
				expected = "previous_invalid"
			case "overlap":
				other := b
				other.Start = 150
				mock.pages[""] = CostPage{Buckets: []CostBucket{b, other}}
				expected = "overlapping_bucket"
			case "page_limit":
				for i := range 100 {
					cursor := ""
					if i > 0 {
						cursor = fmt.Sprint(i)
					}
					mock.pages[cursor] = CostPage{Next: fmt.Sprint(i + 1)}
				}
				expected = "pagination_limit"
			case "cancel_after_read":
				source = cancellingCostSource{CostPage{Buckets: []CostBucket{b}}, cancel}
				expected = "cancelled"
			case "valid_previous":
				previous = []CostBucket{b}
			}
			if source == nil {
				source = mock
			}
			result, err := CollectCostSnapshot(ctx, source, "org", "project", 100, 200, time.Unix(300, 0), previous)
			if expected == "cancelled" {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, expected, result.Alert)
			require.Equal(t, expected == "", result.Complete)
			require.False(t, result.ProviderHardLimitVerified)
		})
	}
}
