package service

import (
	"context"
	"errors"
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
			if scenario == "complete" || scenario == "duplicate" {
				require.True(t, result.Complete)
				require.Len(t, result.Buckets, 1)
			} else {
				require.False(t, result.Complete)
			}
		})
	}
}
