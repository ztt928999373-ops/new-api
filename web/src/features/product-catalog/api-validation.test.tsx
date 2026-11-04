// Copyright (c) QuantumNous and contributors.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen } from '@testing-library/react'
import { AxiosHeaders, type AxiosResponse } from 'axios'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import catalog from '../../../../service/productcatalog/product_catalog.json'
import { useProductCatalog } from './api'
import { CatalogRequestError } from './request-error'
import { catalogResponseSchema } from './schema'

const clients: QueryClient[] = []
function Probe() {
  const query = useProductCatalog()
  if (query.isPending) return <p>Loading</p>
  if (query.isError) {
    return (
      <CatalogRequestError
        error={query.error}
        onRetry={() => {
          void query.refetch()
        }}
      />
    )
  }
  return <p>Models: {query.data.models.length}</p>
}
function show() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  clients.push(client)
  return render(
    <QueryClientProvider client={client}>
      <Probe />
    </QueryClientProvider>
  )
}
function response(data: unknown): AxiosResponse<unknown> {
  return {
    data,
    status: 200,
    statusText: 'OK',
    headers: {},
    config: { headers: new AxiosHeaders() },
  }
}
afterEach(() => {
  clients.forEach((client) => client.clear())
  clients.length = 0
  vi.restoreAllMocks()
})
const invalid = [
  undefined,
  null,
  {},
  { success: true },
  { success: true, data: null },
  { success: true, data: {} },
  { success: true, data: { ...catalog, models: null } },
  { success: true, data: { ...catalog, models: {} } },
  { success: true, data: { ...catalog, models: [null] } },
  {
    success: true,
    data: { ...catalog, models: [{ public_model_id: 'broken' }] },
  },
  {
    success: true,
    data: {
      ...catalog,
      models: [{ ...catalog.models[0], platform_support: null }],
    },
  },
  {
    success: true,
    data: {
      ...catalog,
      models: [
        {
          ...catalog.models[0],
          official_reference_prices: {
            ...catalog.models[0].official_reference_prices,
            quotes: null,
          },
        },
      ],
    },
  },
]
describe('catalog response validation', () => {
  it.each(invalid)(
    'reports invalid successful responses as controlled errors (%#)',
    async (payload) => {
      vi.spyOn(api, 'get').mockResolvedValue(response(payload))
      show()
      expect(
        await screen.findByText('Model data could not be loaded')
      ).toBeVisible()
      expect(screen.queryByText(/^Models:/)).toBeNull()
    }
  )
  it('preserves valid nullable prices and retries a real query state', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValueOnce(response({ success: true, data: null }))
      .mockResolvedValue(response({ success: true, data: catalog }))
    show()
    await screen.findByText('Model data could not be loaded')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Models: 4')).toBeVisible()
    expect(get).toHaveBeenCalledTimes(2)
  })
})

it('accepts empty catalogs without inventing data and preserves null and zero prices', () => {
  const empty = catalogResponseSchema.parse({
    success: true,
    data: { ...catalog, models: [] },
  })
  expect(empty.data.models).toEqual([])
  const original = catalog.models[0]
  const prices = {
    input: '0',
    output: null,
    cache_read: null,
    cache_write: null,
  }
  const result = catalogResponseSchema.parse({
    success: true,
    data: {
      ...catalog,
      models: [
        {
          ...original,
          platform_selling_prices: {
            ...original.platform_selling_prices,
            prices,
            state: 'configured',
            currency: 'USD',
            unit: 'per_1m_tokens',
            source: 'new_api_effective_pricing',
            pricing_version: 'test-v1',
            updated_on: '2026-10-01',
          },
        },
      ],
    },
  })
  expect(result.data.models[0]?.platform_selling_prices.prices).toEqual(prices)
  expect(
    catalogResponseSchema.safeParse({ success: false, data: catalog }).success
  ).toBe(false)
})
