// Copyright (c) QuantumNous and contributors.
import { render, screen, fireEvent } from '@testing-library/react'
import { AxiosError } from 'axios'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import catalog from '../../../../service/productcatalog/product_catalog.json'
import { CatalogMarketplace, CatalogModelDetails } from './index'

const state = vi.hoisted(() => ({
  id: 'legacy-model',
  catalog: {
    isPending: false,
    isError: true,
    error: new Error('load failed'),
    data: undefined as { models: unknown[] } | undefined,
    refetch: vi.fn(),
  },
  pricing: {
    models: [{ model_name: 'legacy-model' }],
    isLoading: false,
    error: null as Error | null,
    refetch: vi.fn(),
  },
}))
vi.mock('./api', () => ({ useProductCatalog: () => state.catalog }))
vi.mock('@/features/pricing/hooks/use-pricing-data', () => ({
  usePricingData: () => state.pricing,
}))
vi.mock('@tanstack/react-router', () => ({
  useParams: () => ({ modelId: state.id }),
  Link: () => null,
}))
vi.mock('@/components/layout', () => ({
  PublicLayout: (props: { children: React.ReactNode }) => props.children,
}))
vi.mock('@/features/pricing/components/model-details', () => ({
  ModelDetails: () => <p>Legacy model details</p>,
}))

beforeEach(() => {
  sessionStorage.clear()
  state.id = 'legacy-model'
  state.catalog.isPending = false
  state.catalog.isError = true
  state.catalog.error = new Error('load failed')
  state.catalog.data = undefined
  state.pricing.models = [{ model_name: 'legacy-model' }]
  state.pricing.error = null
  state.pricing.isLoading = false
  vi.clearAllMocks()
})
describe('independent model details and request states', () => {
  it('shows legacy details while catalog fails', () => {
    render(<CatalogModelDetails />)
    expect(screen.getByText('Legacy model details')).toBeVisible()
  })
  it('shows legacy details without waiting for catalog', () => {
    state.catalog.isPending = true
    render(<CatalogModelDetails />)
    expect(screen.getByText('Legacy model details')).toBeVisible()
  })
  it('shows catalog details while legacy pricing fails', () => {
    state.id = catalog.models[0].public_model_id
    state.catalog.isError = false
    state.catalog.data = catalog
    state.pricing.models = []
    state.pricing.error = new Error('legacy offline')
    render(<CatalogModelDetails />)
    expect(
      screen.getByRole('heading', { name: catalog.models[0].name })
    ).toBeVisible()
  })
  it('keeps known catalog details when a legacy model uses the same public ID', () => {
    state.id = catalog.models[0].public_model_id
    state.catalog.isError = false
    state.catalog.data = catalog
    state.pricing.models = [{ model_name: state.id }]
    render(<CatalogModelDetails />)
    expect(
      screen.getByRole('heading', { name: catalog.models[0].name })
    ).toBeVisible()
  })
  it('distinguishes unavailable service and retries both sources', () => {
    state.pricing.models = []
    state.catalog.error = new AxiosError('offline', 'ERR_NETWORK')
    render(<CatalogModelDetails />)
    expect(screen.getByText('Service temporarily unavailable')).toBeVisible()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(state.catalog.refetch).toHaveBeenCalled()
    expect(state.pricing.refetch).toHaveBeenCalled()
  })
  it('distinguishes a request failure from an empty catalog', () => {
    render(<CatalogMarketplace />)
    expect(screen.getByText('Model data could not be loaded')).toBeVisible()
  })
  it('does not call successful empty data a loading error', () => {
    state.catalog.isError = false
    state.catalog.data = { models: [] }
    render(<CatalogMarketplace />)
    expect(screen.getByText('No catalog models available')).toBeVisible()
    expect(screen.queryByText('Model data could not be loaded')).toBeNull()
  })
})
