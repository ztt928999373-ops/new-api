// Copyright (c) QuantumNous and contributors.
package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPricingDisclosesFallbackWithoutChangingBilling(t *testing.T) {
	resetPricingEndpointTestTables(t)
	ratios, err := common.Marshal(ratio_setting.GetModelRatioCopy())
	require.NoError(t, err)
	prices, err := common.Marshal(ratio_setting.GetModelPriceCopy())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratios)))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(prices)))
		InvalidatePricingCache()
	})
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"stability-free":0,"stability-priced":1.25}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	insertPricingEndpointChannel(t, 901, 1, dto.ChannelOtherSettings{})
	for _, name := range []string{"stability-free", "stability-priced", "stability-unknown"} {
		insertPricingEndpointAbility(t, 901, name)
	}
	InitChannelCache()
	InvalidatePricingCache()
	found := map[string]bool{}
	for _, price := range GetPricing() {
		if price.ModelName != "stability-free" && price.ModelName != "stability-priced" && price.ModelName != "stability-unknown" {
			continue
		}
		found[price.ModelName] = true
		encoded, err := common.Marshal(price)
		require.NoError(t, err)
		var wire map[string]any
		require.NoError(t, common.Unmarshal(encoded, &wire))
		assert.Equal(t, price.ModelName != "stability-unknown", wire["price_configured"])
		if price.ModelName == "stability-unknown" {
			assert.Equal(t, 37.5, price.ModelRatio)
		}
	}
	require.Len(t, found, 3)
}
