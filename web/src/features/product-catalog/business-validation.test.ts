// Copyright (c) QuantumNous and contributors.
import { describe, expect, it } from 'vitest'

import catalog from '../../../../service/productcatalog/product_catalog.json'
import { catalogResponseSchema } from './schema'

function sample() {
  return structuredClone(catalog.models[0])
}
function valid(model: ReturnType<typeof sample>) {
  return catalogResponseSchema.safeParse({
    success: true,
    data: { ...catalog, models: [model] },
  }).success
}
function configured(model: ReturnType<typeof sample>) {
  Object.assign(model.platform_selling_prices, {
    state: 'configured',
    currency: 'USD',
    unit: 'per_1m_tokens',
    source: 'new_api_effective_pricing',
    pricing_version: 'test-v1',
    updated_on: '2026-10-01',
    prices: { input: '0', output: '2', cache_read: null, cache_write: null },
  })
}
function verified(model: ReturnType<typeof sample>) {
  Object.assign(model.supply, {
    mode: 'official_direct',
    supplier_name: 'Test supplier',
  })
  for (const state of [
    model.platform_support.overall,
    ...Object.values(model.platform_support.features),
  ]) {
    Object.assign(state, {
      state: 'supplier_verified',
      kind: 'supplier',
      evidence_id: 'test-evidence',
      tested_on: '2026-10-01',
    })
  }
}
describe('public catalog business states', () => {
  it('accepts the existing catalog', () => {
    expect(
      catalogResponseSchema.safeParse({ success: true, data: catalog }).success
    ).toBe(true)
  })
  it('keeps selling price independent of supplier and verification', () => {
    const model = sample()
    configured(model)
    expect(valid(model)).toBe(true)
    Object.assign(model.supply, {
      mode: 'approved_aggregator',
      supplier_name: 'Test supplier',
    })
    expect(valid(model)).toBe(true)
    verified(model)
    expect(valid(model)).toBe(true)
    Object.assign(
      model.platform_selling_prices,
      sample().platform_selling_prices
    )
    expect(valid(model)).toBe(true)
  })
  it.each([
    'state',
    'currency',
    'unit',
    'source',
    'pricing_version',
    'updated_on',
  ])('rejects invalid configured pricing %s', (field) => {
    const model = sample()
    configured(model)
    Object.assign(model.platform_selling_prices, {
      [field]: field === 'pricing_version' ? '' : 'invalid',
    })
    expect(valid(model)).toBe(false)
  })
  it('rejects unconfigured pricing carrying zero prices or provenance', () => {
    const model = sample()
    Object.assign(model.platform_selling_prices.prices, { input: '0' })
    expect(valid(model)).toBe(false)
    Object.assign(model.platform_selling_prices.prices, { input: null })
    Object.assign(model.platform_selling_prices, {
      source: 'new_api_effective_pricing',
    })
    expect(valid(model)).toBe(false)
  })
  it('rejects an unassigned supplier with a supplier label', () => {
    const model = sample()
    Object.assign(model.supply, { supplier_name: 'Test supplier' })
    expect(valid(model)).toBe(false)
  })
  it('rejects supplier verification without assignment', () => {
    const model = sample()
    verified(model)
    Object.assign(model.supply, { mode: 'unassigned', supplier_name: null })
    expect(valid(model)).toBe(false)
  })
  it('rejects feature supplier verification without assignment', () => {
    const model = sample()
    Object.assign(model.platform_support.features.streaming, {
      state: 'supplier_verified',
      kind: 'supplier',
      evidence_id: 'test',
      tested_on: '2026-10-01',
    })
    expect(valid(model)).toBe(false)
  })
  it('rejects overall verification that overstates feature evidence', () => {
    const model = sample()
    verified(model)
    Object.assign(
      model.platform_support.features.streaming,
      sample().platform_support.features.streaming
    )
    expect(valid(model)).toBe(false)
  })
  it('rejects mock evidence passed off as supplier verification', () => {
    const model = sample()
    verified(model)
    Object.assign(model.platform_support.overall, { kind: 'mock' })
    expect(valid(model)).toBe(false)
  })
  it('rejects missing evidence and unverified passing claims', () => {
    const model = sample()
    verified(model)
    Object.assign(model.platform_support.overall, { evidence_id: null })
    expect(valid(model)).toBe(false)
    Object.assign(model.platform_support.overall, {
      state: 'unverified',
      kind: 'mock',
    })
    expect(valid(model)).toBe(false)
  })
  it('accepts mock evidence without a real supplier assignment', () => {
    const model = sample()
    Object.assign(model.platform_support.overall, {
      state: 'mock_passed',
      kind: 'mock',
      evidence_id: 'test-mock',
      tested_on: '2026-10-01',
    })
    expect(valid(model)).toBe(true)
  })
})
