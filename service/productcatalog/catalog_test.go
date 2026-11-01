package productcatalog

import (
	"encoding/json" // Test codec only; production callers inject common.Unmarshal.
	"strings"
	"testing"
	"time"

	catalog "github.com/QuantumNous/new-api/types/productcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var reviewDay = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func fixture(t *testing.T) catalog.Catalog {
	t.Helper()
	c, err := LoadEmbedded(json.Unmarshal, reviewDay)
	require.NoError(t, err)
	return c
}

func TestLaunchMatrixAndUnconfiguredPlatform(t *testing.T) {
	c := fixture(t)
	require.Len(t, c.Models, 4)
	manufacturers := make(map[string]bool)
	for _, m := range c.Models {
		manufacturers[m.Manufacturer] = true
		assert.Empty(t, m.InternalRoutes)
		assert.Equal(t, "unassigned", m.Supply.Mode)
		assert.Nil(t, m.Supply.SupplierName)
		assert.Equal(t, catalog.Unverified, m.PlatformSupport.Overall.State)
		assert.Equal(t, "unconfigured", m.PlatformSellingPrices.State)
		assert.Equal(t, catalog.TokenPrices{}, m.PlatformSellingPrices.Prices)
		for _, state := range m.PlatformSupport.Features {
			assert.Equal(t, catalog.Unverified, state.State)
		}
		assert.NotEmpty(t, m.OfficialReferencePrices.Quotes)
	}
	assert.Len(t, manufacturers, 3)
	assert.True(t, manufacturers["openai"] && manufacturers["anthropic"] && manufacturers["google"])
}

func TestUnknownExplicitZeroAndFalseRemainDistinct(t *testing.T) {
	c := fixture(t)
	zero := "0"
	c.Models[0].OfficialReferencePrices.Quotes[0].Prices.Input = nil
	c.Models[0].OfficialReferencePrices.Quotes[0].Prices.Output = &zero
	c.Models[0].ManufacturerFacts.Capabilities.ImageInput = nil
	require.NoError(t, Validate(c, reviewDay))
	view := PublicView(c)
	raw, err := json.Marshal(view)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(raw, &wire))
	first := wire["models"].([]any)[0].(map[string]any)
	quotes := first["official_reference_prices"].(map[string]any)["quotes"].([]any)
	p := quotes[0].(map[string]any)["prices"].(map[string]any)
	assert.Nil(t, p["input"])
	assert.Equal(t, "0", p["output"])
	caps := first["manufacturer_facts"].(map[string]any)["capabilities"].(map[string]any)
	assert.Nil(t, caps["image_input"])
	assert.Equal(t, false, caps["image_output"])
	for _, field := range []string{"input", "output", "cache_read", "cache_write"} {
		assert.Contains(t, first["platform_selling_prices"].(map[string]any)["prices"], field)
		assert.Nil(t, first["platform_selling_prices"].(map[string]any)["prices"].(map[string]any)[field])
	}
}

func TestPrivateAssignmentsNeverEnterPublicJSON(t *testing.T) {
	c := fixture(t)
	c.Models[0].InternalRoutes = []catalog.InternalRoute{{ActualSupplierID: "private-supplier-sentinel", ChannelID: 912345, InternalModelID: "private-upstream-sentinel", ConfigFingerprint: "private-config-sentinel"}}
	require.NoError(t, Validate(c, reviewDay))
	for _, value := range []any{c, PublicView(c)} {
		wire, err := json.Marshal(value)
		require.NoError(t, err)
		for _, secret := range []string{"private-supplier-sentinel", "private-upstream-sentinel", "private-config-sentinel", "912345", "internal_routes", "api_key", "base_url"} {
			assert.NotContains(t, string(wire), secret)
		}
	}
	assert.Equal(t, "openai", c.Models[0].Manufacturer)
	assert.Equal(t, "gpt-5.4-mini", c.Models[0].PublicModelID)
}

func TestReferencePriceDatesAndAlternativeCacheTTLs(t *testing.T) {
	c := fixture(t)
	google := c.Models[3].OfficialReferencePrices
	for _, test := range []struct{ date, input string }{{"2026-12-31", "0.75"}, {"2027-01-01", "1.50"}} {
		t.Run(test.date, func(t *testing.T) {
			quotes, err := ReferenceQuotesOn(google, test.date)
			require.NoError(t, err)
			require.Len(t, quotes, 1)
			assert.Equal(t, test.input, *quotes[0].Prices.Input)
		})
	}
	past, err := ReferenceQuotesOn(google, "2026-10-01")
	require.NoError(t, err)
	assert.Empty(t, past)
	_, err = ReferenceQuotesOn(google, "invalid-date")
	assert.Error(t, err)
	claude := c.Models[1].OfficialReferencePrices
	quotes, err := ReferenceQuotesOn(claude, "2026-11-01")
	require.NoError(t, err)
	require.Len(t, quotes, 2)
	assert.Equal(t, int64(300), *quotes[0].CacheWriteTTLSeconds)
	assert.Equal(t, "2.50", *quotes[0].Prices.CacheWrite)
	assert.Equal(t, int64(3600), *quotes[1].CacheWriteTTLSeconds)
	assert.Equal(t, "4.00", *quotes[1].Prices.CacheWrite)
	assert.Nil(t, c.Models[2].OfficialReferencePrices.Quotes[0].Prices.CacheWrite)
	assert.Equal(t, "per_1m_token_hours", c.Models[2].OfficialReferencePrices.AdditionalCharges[0].Unit)
}

func TestValidationRejectsMisleadingCatalogs(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*catalog.Catalog)
	}{
		{"duplicate model", func(c *catalog.Catalog) { c.Models[1].PublicModelID = c.Models[0].PublicModelID }},
		{"unknown schema", func(c *catalog.Catalog) { c.SchemaVersion = 2 }},
		{"missing sources", func(c *catalog.Catalog) { c.Models[0].Sources = nil }},
		{"unofficial source", func(c *catalog.Catalog) { c.Models[0].Sources[0].URL = "https://third-party.invalid/spec" }},
		{"source credential", func(c *catalog.Catalog) {
			c.Models[0].Sources[0].URL = "https://user:secret@developers.openai.com/spec"
		}},
		{"source query secret", func(c *catalog.Catalog) { c.Models[0].Sources[0].URL += "?api_key=synthetic" }},
		{"unknown source ref", func(c *catalog.Catalog) { c.Models[0].OfficialReferencePrices.SourceIDs = []string{"missing"} }},
		{"future check", func(c *catalog.Catalog) { c.Models[0].Sources[0].CheckedOn = "2026-11-02" }},
		{"invalid limits", func(c *catalog.Catalog) { n := int64(-1); c.Models[0].ManufacturerFacts.Limits.ContextTokens = &n }},
		{"output exceeds context", func(c *catalog.Catalog) { n := int64(1); c.Models[0].ManufacturerFacts.Limits.ContextTokens = &n }},
		{"negative price", func(c *catalog.Catalog) { s := "-1"; c.Models[0].OfficialReferencePrices.Quotes[0].Prices.Input = &s }},
		{"NaN price", func(c *catalog.Catalog) { s := "NaN"; c.Models[0].OfficialReferencePrices.Quotes[0].Prices.Input = &s }},
		{"inverted period", func(c *catalog.Catalog) {
			s := "2028-01-01"
			c.Models[3].OfficialReferencePrices.Quotes[0].EffectiveFrom = &s
		}},
		{"overlapping periods", func(c *catalog.Catalog) {
			s := "2026-12-01"
			c.Models[3].OfficialReferencePrices.Quotes[1].EffectiveFrom = &s
		}},
		{"missing feature", func(c *catalog.Catalog) { delete(c.Models[0].PlatformSupport.Features, "streaming") }},
		{"unknown state", func(c *catalog.Catalog) { c.Models[0].PlatformSupport.Overall.State = "supported" }},
		{"invented supplier", func(c *catalog.Catalog) { s := "OpenAI"; c.Models[0].Supply.SupplierName = &s }},
		{"reference used as sale", func(c *catalog.Catalog) { s := "0.75"; c.Models[0].PlatformSellingPrices.Prices.Input = &s }},
		{"incomplete route", func(c *catalog.Catalog) { c.Models[0].InternalRoutes = []catalog.InternalRoute{{ChannelID: 1}} }},
		{"nontext launch", func(c *catalog.Catalog) { c.Models[0].LaunchOutputModalities = []string{"image"} }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) { c := fixture(t); test.mutate(&c); assert.Error(t, Validate(c, reviewDay)) })
	}
}

func TestVerificationStatesDoNotPromoteMockToSupplier(t *testing.T) {
	kindMock, kindSupplier, evidence, date := "mock", "supplier", "fixture-evidence", "2026-11-01"
	cases := []struct {
		name  string
		value catalog.Verification
		valid bool
	}{
		{"unverified", catalog.Verification{State: catalog.Unverified, Notes: "Not tested"}, true},
		{"mock", catalog.Verification{State: catalog.MockPassed, Kind: &kindMock, EvidenceID: &evidence, TestedOn: &date, Notes: "Mock only"}, true},
		{"supplier", catalog.Verification{State: catalog.SupplierVerified, Kind: &kindSupplier, EvidenceID: &evidence, TestedOn: &date, Notes: "Recorded supplier test"}, true},
		{"partial", catalog.Verification{State: catalog.Partial, Kind: &kindMock, EvidenceID: &evidence, TestedOn: &date, Notes: "Some fixture features unsupported"}, true},
		{"unsupported", catalog.Verification{State: catalog.Unsupported, Notes: "Explicitly not offered"}, true},
		{"fake supplier proof", catalog.Verification{State: catalog.SupplierVerified, Kind: &kindMock, EvidenceID: &evidence, TestedOn: &date, Notes: "Mock is not provider evidence"}, false},
		{"unverified with pass", catalog.Verification{State: catalog.Unverified, Kind: &kindMock, EvidenceID: &evidence, TestedOn: &date, Notes: "Contradiction"}, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := validateVerification(test.value, reviewDay)
			if test.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
	c := fixture(t)
	c.Models[0].PlatformSupport.Overall = catalog.Verification{State: catalog.SupplierVerified, Kind: &kindSupplier, EvidenceID: &evidence, TestedOn: &date, Notes: "No configured route exists"}
	assert.Error(t, Validate(c, reviewDay))
}

func TestLoadsAreIndependentAndDoNotRequireHostInitialization(t *testing.T) {
	first := fixture(t)
	first.Models[0].ManufacturerFacts.Notes[0] = "caller mutation"
	second := fixture(t)
	assert.False(t, strings.Contains(second.Models[0].ManufacturerFacts.Notes[0], "caller mutation"))
	_, err := LoadEmbedded(nil, reviewDay)
	require.Error(t, err)
	_, err = Load([]byte("not JSON"), json.Unmarshal, reviewDay)
	require.Error(t, err)
}

func TestPublicDocumentRejectsPrivateAndMisspelledFields(t *testing.T) {
	for _, field := range []string{"internal_routes", "api_key", "actual_supplier_id", "internal_model_id", "base_url", "capabilites"} {
		t.Run(field, func(t *testing.T) {
			var wire map[string]any
			require.NoError(t, json.Unmarshal(embedded, &wire))
			wire["models"].([]any)[0].(map[string]any)[field] = "synthetic-private-value"
			raw, err := json.Marshal(wire)
			require.NoError(t, err)
			_, err = Load(raw, json.Unmarshal, reviewDay)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "synthetic-private-value")
		})
	}
}
