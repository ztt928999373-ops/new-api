// Copyright (c) QuantumNous and contributors.
import { expect, it } from 'vitest'

import { sortModels } from '../lib/filters'
import type { PricingModel } from '../types'

const base: PricingModel = {
  id: 1,
  model_name: 'base',
  quota_type: 0,
  model_ratio: 1,
  completion_ratio: 1,
  enable_groups: ['default'],
}
for (const kind of ['input', 'output', 'request'] as const) {
  for (const direction of ['low', 'high'] as const) {
    it(`keeps every unknown ${kind} price last when sorting ${direction}`, () => {
      const priceModel = (name: string, value: unknown): PricingModel => {
        if (kind === 'request') {
          return {
            ...base,
            model_name: name,
            quota_type: 1,
            model_price: value as number,
          }
        }
        if (kind === 'output') {
          return {
            ...base,
            model_name: name,
            completion_ratio: value as number,
          }
        }
        return { ...base, model_name: name, model_ratio: value as number }
      }
      const unknown = [undefined, null, Number.NaN, Infinity, -Infinity].map(
        (value, index) => priceModel(`unknown-${index}`, value)
      )
      const unconfigured = {
        ...priceModel('unconfigured', 37.5),
        price_configured: false,
      }
      const dynamic: PricingModel = {
        ...priceModel('dynamic', 37.5),
        price_configured: true,
        price_configuration: 'dynamic',
      }
      const models = [
        unknown[0],
        priceModel('high', 5),
        unknown[1],
        priceModel('zero', 0),
        ...unknown.slice(2),
        priceModel('low', 2),
        unconfigured,
        dynamic,
      ]
      const before = models.map((m) => m.model_name)
      expect(
        sortModels(models, `${kind}-price-${direction}`).map(
          (m) => m.model_name
        )
      ).toEqual([
        ...(direction === 'low'
          ? ['zero', 'low', 'high']
          : ['high', 'low', 'zero']),
        ...unknown.map((m) => m.model_name),
        'unconfigured',
        'dynamic',
      ])
      expect(models.map((m) => m.model_name)).toEqual(before)
    })
  }
}
