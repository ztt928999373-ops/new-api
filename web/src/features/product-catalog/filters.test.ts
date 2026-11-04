// Copyright (c) QuantumNous and contributors.
import { describe, expect, it } from 'vitest'

import catalog from '../../../../service/productcatalog/product_catalog.json'
import { filterCatalogModels } from './filters'

describe('launch catalog filters', () => {
  it('searches public identity and manufacturer without claiming availability', () => {
    expect(
      filterCatalogModels(catalog.models, ' OPENAI ', null).map(
        (m) => m.manufacturer
      )
    ).toEqual(['openai'])
    expect(
      filterCatalogModels(
        catalog.models,
        catalog.models[0].public_model_id,
        null
      )
    ).toHaveLength(1)
    expect(
      filterCatalogModels(catalog.models, 'nonexistent-model', null)
    ).toEqual([])
  })
  it('requires explicit official support, without inferring platform verification', () => {
    const models = filterCatalogModels(catalog.models, '', 'image_input')
    expect(models.length).toBeGreaterThan(0)
    expect(
      models.every(
        (m) => m.manufacturer_facts.capabilities.image_input === true
      )
    ).toBe(true)
    expect(
      models.every((m) => m.platform_support.overall.state === 'unverified')
    ).toBe(true)
    expect(
      filterCatalogModels(catalog.models, '', 'unknown_capability')
    ).toEqual([])
  })
})
