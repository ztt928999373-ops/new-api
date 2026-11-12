package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

// MockHTTPCostSource cannot target a supplier. Only literal loopback HTTP is
// accepted; there is deliberately no production constructor or credential input.
type MockHTTPCostSource struct {
	endpoint              string
	organization, project string
	start, end            int64
	client                *http.Client
}

func NewMockHTTPCostSource(endpoint, organization, project string, start, end int64) (*MockHTTPCostSource, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("invalid mock cost endpoint")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.IsLoopback() || u.Scheme != "http" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || organization == "" || project == "" || start < 0 || end <= start {
		return nil, errors.New("invalid mock cost endpoint")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, DisableKeepAlives: true, ResponseHeaderTimeout: 2 * time.Second, MaxResponseHeaderBytes: 32 << 10}
	return &MockHTTPCostSource{endpoint: u.Scheme + "://" + u.Host, organization: organization, project: project, start: start, end: end, client: &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (s *MockHTTPCostSource) ReadCosts(ctx context.Context, cursor string) (CostPage, error) {
	bad := errors.New("mock cost response rejected")
	if len(cursor) > 1024 {
		return CostPage{}, bad
	}
	values := url.Values{"start_time": {strconv.FormatInt(s.start, 10)}, "end_time": {strconv.FormatInt(s.end, 10)}, "bucket_width": {"1d"}, "group_by": {"project_id"}, "project_ids": {s.project}, "limit": {"31"}}
	if cursor != "" {
		values.Set("page", cursor)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint+"/v1/organization/costs?"+values.Encode(), nil)
	if err != nil {
		return CostPage{}, bad
	}
	request.Header.Set("Authorization", "Bearer synthetic-readonly-costs")
	response, err := s.client.Do(request)
	if err != nil {
		return CostPage{}, bad
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return CostPage{}, bad
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return CostPage{}, bad
	}
	var body struct {
		Object  string `json:"object"`
		HasMore bool   `json:"has_more"`
		Next    string `json:"next_page"`
		Data    []struct {
			Start   int64 `json:"start_time"`
			End     int64 `json:"end_time"`
			Results []struct {
				Object  string `json:"object"`
				Project string `json:"project_id"`
				Amount  struct {
					Currency string      `json:"currency"`
					Value    json.Number `json:"value"`
				} `json:"amount"`
			} `json:"results"`
		} `json:"data"`
	}
	if common.Unmarshal(data, &body) != nil || body.Object != "page" || len(body.Data) > 1000 || len(body.Next) > 1024 || body.HasMore != (body.Next != "") {
		return CostPage{}, bad
	}
	page := CostPage{Next: body.Next}
	for _, bucket := range body.Data {
		if len(bucket.Results) != 1 || bucket.Start < s.start || bucket.End > s.end || bucket.End <= bucket.Start {
			return CostPage{}, bad
		}
		result := bucket.Results[0]
		raw := result.Amount.Value.String()
		if len(raw) == 0 || len(raw) > 32 {
			return CostPage{}, bad
		}
		// Bound exponent before decimal parsing to avoid giant coefficient shifts.
		for _, c := range raw {
			if c == 'e' || c == 'E' {
				return CostPage{}, bad
			}
		}
		value, err := decimal.NewFromString(raw)
		if err != nil || value.IsNegative() || result.Amount.Currency != "usd" || result.Project != s.project || result.Object != "organization.costs.result" {
			return CostPage{}, bad
		}
		micros := value.Mul(decimal.NewFromInt(1000000))
		if !micros.Equal(micros.Truncate(0)) || micros.GreaterThan(decimal.NewFromInt(9_000_000_000_000_000)) {
			return CostPage{}, bad
		}
		page.Buckets = append(page.Buckets, CostBucket{Organization: s.organization, Project: s.project, Start: bucket.Start, End: bucket.End, USDmicros: micros.IntPart()})
	}
	return page, nil
}
