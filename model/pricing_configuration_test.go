// Copyright (c) QuantumNous and contributors.
package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestPricingDisclosesStaticAndDynamicConfigurations(t *testing.T) {
	resetPricingEndpointTestTables(t)
	source := strings.ReplaceAll(pricingUsagePluginSource("1.0.0", `{seconds:{type:"number",unit:"second"}}`), "pricing-usage-probe", "r2-price-plugin")
	source = strings.ReplaceAll(source, "pricing-usage-model", "r2-plugin")
	_, pluginErr := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, pluginErr)
	t.Cleanup(func() { require.NoError(t, jsplugin.DefaultRegistry.Unregister("r2-price-plugin")) })
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(k, v string) error { saved[k] = v; return nil }))
	ratios, err := common.Marshal(ratio_setting.GetModelRatioCopy())
	require.NoError(t, err)
	prices, err := common.Marshal(ratio_setting.GetModelPriceCopy())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratios)))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(prices)))
		InvalidatePricingCache()
	})
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"r2-static":1.25,"r2-mixed":2}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"r2-free":0,"r2-request":3}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		billing_setting.PluginBillingExprOption: `{"r2-price-plugin::r2-plugin":"tier(\"duration\", u(\"seconds\") * 2)"}`,
		"billing_setting.billing_mode":          `{"r2-dynamic":"tiered_expr","r2-mixed":"tiered_expr","r2-empty":"tiered_expr"}`,
		"billing_setting.billing_expr":          `{"r2-dynamic":"tier(\"standard\", p * 2 + c * 3)","r2-mixed":"tier(\"standard\", p * 2 + c * 3)","r2-empty":" "}`,
	}))
	insertPricingEndpointChannel(t, 902, 1, dto.ChannelOtherSettings{})
	cases := map[string]string{"r2-plugin": "dynamic", "r2-static": "static", "r2-mixed": "mixed", "r2-dynamic": "dynamic", "r2-free": "static", "r2-request": "static", "r2-empty": "unconfigured", "r2-unknown": "unconfigured"}
	for name := range cases {
		insertPricingEndpointAbility(t, 902, name)
	}
	InitChannelCache()
	InvalidatePricingCache()
	got := pricingByModel(GetPricing())
	for name, kind := range cases {
		t.Run(name, func(t *testing.T) {
			price, ok := got[name]
			require.True(t, ok)
			data, err := common.Marshal(price)
			require.NoError(t, err)
			var wire map[string]any
			require.NoError(t, common.Unmarshal(data, &wire))
			assert.Equal(t, kind, wire["price_configuration"])
			assert.Equal(t, kind != "unconfigured", wire["price_configured"])
			if name == "r2-dynamic" || kind == "mixed" {
				assert.Equal(t, `tier("standard", p * 2 + c * 3)`, price.BillingExpr)
			}
			if name == "r2-plugin" {
				assert.Empty(t, price.BillingExpr)
				require.Len(t, price.BillingPluginVariants, 1)
				assert.Equal(t, `tier("duration", u("seconds") * 2)`, price.BillingPluginVariants[0].BillingExpr)
			}
			if name == "r2-unknown" {
				assert.Equal(t, 37.5, price.ModelRatio)
			}
			if name == "r2-free" {
				assert.Equal(t, 0.0, price.ModelPrice)
			}
		})
	}
}
