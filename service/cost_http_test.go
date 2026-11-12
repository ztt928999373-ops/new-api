package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMockHTTPCostSource(t *testing.T) {
	for _, scenario := range []string{"valid", "redirect", "oversized", "currency", "precision", "exponent", "project", "paging", "cancelled", "timeout", "unauthorized", "duplicate_key", "missing_paging", "missing_time"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if scenario == "timeout" {
					<-r.Context().Done()
					return
				}
				require.Equal(t, "GET", r.Method)
				require.Equal(t, "/v1/organization/costs", r.URL.Path)
				require.Equal(t, "Bearer synthetic-readonly-costs", r.Header.Get("Authorization"))
				require.Equal(t, "cursor", r.URL.Query().Get("page"))
				if scenario == "redirect" {
					http.Redirect(w, r, "http://127.0.0.1:1", 302)
					return
				}
				if scenario == "unauthorized" {
					w.WriteHeader(401)
					return
				}
				if scenario == "oversized" {
					_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
					return
				}
				body := `{"object":"page","has_more":false,"next_page":"","data":[{"start_time":100,"end_time":200,"results":[{"object":"organization.costs.result","project_id":"project","amount":{"currency":"usd","value":0.000007}}]}]}`
				switch scenario {
				case "duplicate_key":
					body = strings.Replace(body, `"value":0.000007`, `"Value":9,"value":0.000007`, 1)
				case "missing_paging":
					body = strings.Replace(body, `"has_more":false,`, "", 1)
				case "missing_time":
					body = strings.Replace(body, `"start_time":100,`, "", 1)
				case "currency":
					body = strings.Replace(body, "usd", "cny", 1)
				case "precision":
					body = strings.Replace(body, "0.000007", "0.0000001", 1)
				case "exponent":
					body = strings.Replace(body, "0.000007", "1e99999999", 1)
				case "project":
					body = strings.Replace(body, `"project"`, `"other"`, 1)
				case "paging":
					body = strings.Replace(body, `"has_more":false`, `"has_more":true`, 1)
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			source, err := NewMockHTTPCostSource(server.URL, "org", "project", 100, 200)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if scenario == "cancelled" {
				cancel()
			}
			page, err := source.ReadCosts(ctx, "cursor")
			if scenario == "valid" {
				require.NoError(t, err)
				require.EqualValues(t, 7, page.Buckets[0].USDmicros)
			} else {
				require.Error(t, err)
			}
			if scenario == "cancelled" {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls, "no retries or redirects")
			}
		})
	}
	for _, target := range []string{"https://api.openai.com", "http://localhost:80", "http://0.0.0.0:80", "http://127.0.0.1:80/path", "http://user:pass@127.0.0.1:80", "http://127.0.0.1:80?x=y"} {
		_, err := NewMockHTTPCostSource(target, "org", "project", 100, 200)
		require.Error(t, err)
	}
}

func TestMockHTTPCompleteSnapshot(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "1d", r.URL.Query().Get("bucket_width"))
		if r.URL.Query().Get("page") == "" {
			_, _ = w.Write([]byte(`{"object":"page","has_more":true,"next_page":"second","data":[{"start_time":100,"end_time":150,"results":[{"object":"organization.costs.result","project_id":"project","amount":{"currency":"usd","value":0.000007}}]}]}`))
		} else {
			require.Equal(t, "second", r.URL.Query().Get("page"))
			_, _ = w.Write([]byte(`{"object":"page","has_more":false,"next_page":null,"data":[{"start_time":150,"end_time":200,"results":[{"object":"organization.costs.result","project_id":"project","amount":{"currency":"usd","value":0.000003}}]}]}`))
		}
	}))
	defer server.Close()
	source, err := NewMockHTTPCostSource(server.URL, "org", "project", 100, 200)
	require.NoError(t, err)
	snapshot, err := CollectCostSnapshot(context.Background(), source, "org", "project", 100, 200, time.Unix(300, 0), nil)
	require.NoError(t, err)
	require.True(t, snapshot.Complete)
	require.Equal(t, 2, calls)
	require.Len(t, snapshot.Buckets, 2)
	require.EqualValues(t, 7, snapshot.Buckets[0].USDmicros)
	require.EqualValues(t, 3, snapshot.Buckets[1].USDmicros)
}
