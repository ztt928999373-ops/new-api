// Copyright (c) QuantumNous and contributors.
import { describe, expect, it } from 'vitest'

import {
  calculateTokenPrice,
  formatPrice,
  formatGroupPrice,
  formatFixedPrice,
  formatRequestPrice,
} from '../lib/price'
import type { PricingModel } from '../types'

const base: PricingModel = {
  id: 1,
  model_name: 'legacy',
  quota_type: 0,
  model_ratio: 37.5,
  completion_ratio: 2,
  enable_groups: ['default'],
}
describe('price display provenance', () => {
  it('does not publish a fallback multiplier as a configured sale price', () => {
    const model = { ...base, price_configured: false }
    expect(calculateTokenPrice(model, 'input', 1)).toBeNaN()
    expect(formatPrice(model, 'input', 'M')).toBe('Price not configured')
    expect(
      formatGroupPrice(model, 'default', 'input', 'M', false, 1, 1, {
        default: 1,
      })
    ).toBe('Price not configured')
  })
  it('does not turn missing per-request price into a free price', () => {
    const model = { ...base, quota_type: 1, model_price: undefined }
    expect(formatRequestPrice(model)).toBe('Pending verification')
    expect(
      formatFixedPrice(model, 'default', false, 1, 1, { default: 1 })
    ).toBe('Pending verification')
  })
  it('does not turn missing output ratios into free output', () => {
    const model = { ...base, completion_ratio: null as unknown as number }
    expect(calculateTokenPrice(model, 'output', 1)).toBeNaN()
    expect(formatPrice(model, 'output', 'M')).toBe('Pending verification')
  })
  it('preserves explicitly configured free prices and legacy numeric prices', () => {
    expect(
      calculateTokenPrice(
        { ...base, model_ratio: 0, price_configured: true },
        'input',
        1
      )
    ).toBe(0)
    expect(
      formatRequestPrice({ ...base, quota_type: 1, model_price: 0 })
    ).not.toBe('Pending verification')
    expect(
      calculateTokenPrice({ ...base, model_ratio: 1.25 }, 'output', 1)
    ).toBe(5)
  })
})

// A price formatter called for the wrong billing type remains not applicable.
describe('price formatter billing-type compatibility', () => {
  it('returns a dash for per-request formatters used with token pricing', () => {
    for (const price_configured of [undefined, false, true]) {
      for (const model_price of [undefined, 0, 2]) {
        const model = { ...base, price_configured, model_price }
        expect(formatRequestPrice(model)).toBe('-')
        expect(
          formatFixedPrice(model, 'default', false, 1, 1, { default: 1 })
        ).toBe('-')
      }
    }
  })
  it('returns a dash for token formatters used with per-request pricing', () => {
    const model = { ...base, quota_type: 1, price_configured: false }
    expect(formatPrice(model, 'input', 'M')).toBe('-')
    expect(
      formatGroupPrice(model, 'default', 'input', 'M', false, 1, 1, {
        default: 1,
      })
    ).toBe('-')
  })
})

describe('dynamic price display provenance', () => {
  it('does not expose retained static defaults for dynamic-only pricing', () => {
    const model: PricingModel = {
      ...base,
      price_configured: true,
      price_configuration: 'dynamic',
    }
    expect(calculateTokenPrice(model, 'input', 1)).toBeNaN()
    expect(formatPrice(model, 'input', 'M')).toBe('Pending verification')
    expect(
      formatGroupPrice(model, 'default', 'input', 'M', false, 1, 1, {
        default: 1,
      })
    ).toBe('Pending verification')
    const request = { ...model, quota_type: 1, model_price: 37.5 }
    expect(formatRequestPrice(request)).toBe('Pending verification')
    expect(
      formatFixedPrice(request, 'default', false, 1, 1, { default: 1 })
    ).toBe('Pending verification')
    expect(
      calculateTokenPrice(
        { ...model, price_configuration: 'mixed', model_ratio: 2 },
        'input',
        1
      )
    ).toBe(4)
  })
})

describe('runtime price value boundaries', () => {
  it.each([
    '',
    ' ',
    false,
    true,
    null,
    undefined,
    Number.NaN,
    Infinity,
    -Infinity,
    'NaN',
    'Infinity',
    [],
    {},
  ])('keeps invalid price %j unknown', (value) => {
    const invalid = value as unknown as number
    expect(
      calculateTokenPrice({ ...base, model_ratio: invalid }, 'input', 1)
    ).toBeNaN()
    expect(
      calculateTokenPrice({ ...base, completion_ratio: invalid }, 'output', 1)
    ).toBeNaN()
    expect(formatPrice({ ...base, model_ratio: invalid }, 'input', 'M')).toBe(
      'Pending verification'
    )
    const request = { ...base, quota_type: 1, model_price: invalid }
    expect(formatRequestPrice(request)).toBe('Pending verification')
    expect(
      formatFixedPrice(request, 'default', false, 1, 1, { default: 1 })
    ).toBe('Pending verification')
  })
  it.each([0, '0', 1.25, '1.25'])(
    'preserves valid numeric price %j',
    (value) => {
      const valid = value as unknown as number
      expect(
        calculateTokenPrice({ ...base, model_ratio: valid }, 'input', 1)
      ).toBe(Number(value) * 2)
      expect(
        calculateTokenPrice({ ...base, completion_ratio: valid }, 'output', 1)
      ).toBe(75 * Number(value))
      const request = { ...base, quota_type: 1, model_price: valid }
      expect(formatRequestPrice(request)).toBe(
        formatRequestPrice({ ...request, model_price: Number(value) })
      )
      expect(formatRequestPrice(request)).not.toBe('Pending verification')
    }
  )
})
