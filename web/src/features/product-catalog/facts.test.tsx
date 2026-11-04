// Copyright (c) QuantumNous and contributors.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import catalog from '../../../../service/productcatalog/product_catalog.json'
import { CatalogFacts } from './facts'

describe('catalog disclosures', () => {
  it('separates reference prices from unknown selling prices and unavailable supply', () => {
    render(<CatalogFacts model={catalog.models[0]} detailed />)
    expect(
      screen.getByText('Supplier not configured. Not callable.')
    ).toBeVisible()
    expect(screen.getByText('Official reference prices')).toBeVisible()
    expect(screen.getByText('Platform selling prices')).toBeVisible()
    expect(screen.getAllByText(/Pending verification/).length).toBeGreaterThan(
      0
    )
    expect(screen.getByText('Official capabilities')).toBeVisible()
    expect(screen.getAllByText('Platform verification').length).toBeGreaterThan(
      0
    )
    expect(
      screen.getAllByRole('link').map((a) => a.getAttribute('href'))
    ).toEqual(catalog.models[0].sources.map((s) => s.url))
  })
})
