import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { AxiosError, AxiosHeaders } from 'axios'
import { afterEach, expect, it, vi } from 'vitest'

import { CatalogModelDetails } from '@/features/product-catalog'
import { api } from '@/lib/api'
import { createAppQueryClient } from '@/lib/query-client'

import catalog from '../../../../../service/productcatalog/product_catalog.json'
import { Pricing } from '../index'

const detailRoute = vi.hoisted(() => ({ modelId: 'retry-model' }))

vi.mock('@visactor/react-vchart', () => ({ VChart: () => null }))
vi.mock('@/components/layout', () => ({
  PublicLayout: (props: { children: React.ReactNode }) => props.children,
}))
vi.mock('@/hooks/use-status', () => ({ useStatus: () => ({ status: {} }) }))
vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  useSearch: () => ({}),
  useParams: () => ({ modelId: detailRoute.modelId }),
  useNavigate: () => vi.fn(),
  Link: (props: { children: React.ReactNode }) => <span>{props.children}</span>,
}))

const clients: ReturnType<typeof createAppQueryClient>[] = []
const pricing = {
  success: true,
  data: [
    {
      id: 1,
      model_name: 'retry-model',
      quota_type: 0,
      model_ratio: 1,
      completion_ratio: 2,
      enable_groups: ['default'],
    },
  ],
  vendors: [],
  group_ratio: { default: 1 },
  usable_group: { default: 'Default' },
  supported_endpoint: {},
  auto_groups: [],
}
function response(data: unknown, status = 200) {
  return {
    data,
    status,
    statusText: 'Test',
    headers: {},
    config: { headers: new AxiosHeaders() },
  }
}
function show(details = false) {
  const redirect = vi.fn()
  const client = createAppQueryClient(redirect)
  client.setDefaultOptions({ queries: { retry: false, gcTime: 0 } })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      {details ? <CatalogModelDetails /> : <Pricing />}
    </QueryClientProvider>
  )
  return { client, redirect }
}
afterEach(() => {
  clients.forEach((client) => client.clear())
  clients.length = 0
  sessionStorage.clear()
  detailRoute.modelId = 'retry-model'
  vi.restoreAllMocks()
})

it('keeps successful empty pricing distinct from a failed request', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (url) =>
    response(
      url === '/api/pricing'
        ? { ...pricing, data: [] }
        : { success: true, data: catalog }
    )
  )
  show()
  expect(await screen.findByText('No models found')).toBeVisible()
  expect(
    screen.queryByText('Model data could not be loaded')
  ).not.toBeInTheDocument()
})

it('shows a local pricing failure and retries to real query success', async () => {
  let failed = true
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url !== '/api/pricing') {
      return response({ success: true, data: catalog })
    }
    return response(
      failed
        ? { success: false, message: 'Synthetic pricing failure' }
        : pricing
    )
  })
  show()
  expect(
    await screen.findByText('Model data could not be loaded')
  ).toBeVisible()
  expect(screen.queryByText('No models found')).not.toBeInTheDocument()
  expect(await screen.findByText('GPT-5.4 Mini')).toBeVisible()
  failed = false
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(
    await screen.findByRole('heading', { name: 'retry-model' })
  ).toBeVisible()
  expect(
    screen.queryByText('Model data could not be loaded')
  ).not.toBeInTheDocument()
})

it.each(['/api/pricing', '/api/product-catalog'])(
  'keeps %s HTTP 500 local, preserves the other source, and retries',
  async (endpoint) => {
    let failed = true
    vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === endpoint && failed) {
        throw new AxiosError(
          'Synthetic HTTP 500',
          'ERR_BAD_RESPONSE',
          undefined,
          undefined,
          response({ success: false }, 500)
        )
      }
      return response(
        url === '/api/pricing' ? pricing : { success: true, data: catalog }
      )
    })
    const { redirect } = show()
    expect(
      await screen.findByText('Model data could not be loaded')
    ).toBeVisible()
    expect(screen.getByRole('heading', { name: 'Model Square' })).toBeVisible()
    if (endpoint === '/api/pricing') {
      expect(await screen.findByText('GPT-5.4 Mini')).toBeVisible()
    } else {
      expect(
        await screen.findByRole('heading', { name: 'retry-model' })
      ).toBeVisible()
    }
    expect(redirect).not.toHaveBeenCalled()
    failed = false
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() =>
      expect(
        screen.queryByText('Model data could not be loaded')
      ).not.toBeInTheDocument()
    )
    expect(await screen.findByText('GPT-5.4 Mini')).toBeVisible()
    expect(screen.getByRole('heading', { name: 'retry-model' })).toBeVisible()
  }
)

it('preserves global HTTP 500 handling for queries that do not opt in', async () => {
  const redirect = vi.fn()
  const client = createAppQueryClient(redirect)
  clients.push(client)
  await expect(
    client.fetchQuery({
      queryKey: ['unrelated'],
      retry: false,
      queryFn: async () => {
        throw new AxiosError(
          '500',
          undefined,
          undefined,
          undefined,
          response({}, 500)
        )
      },
    })
  ).rejects.toThrow('500')
  expect(redirect).toHaveBeenCalledOnce()
})

it('keeps a detail pricing HTTP 500 local and retries without reporting a missing model', async () => {
  let failed = true
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/pricing' && failed) {
      throw new AxiosError(
        'Synthetic HTTP 500',
        undefined,
        undefined,
        undefined,
        response({ success: false }, 500)
      )
    }
    return response(
      url === '/api/pricing' ? pricing : { success: true, data: catalog }
    )
  })
  const { redirect } = show(true)
  expect(
    await screen.findByText('Model data could not be loaded')
  ).toBeVisible()
  expect(screen.queryByText('Model not found')).not.toBeInTheDocument()
  expect(redirect).not.toHaveBeenCalled()
  failed = false
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(
    await screen.findByRole('heading', { name: 'retry-model' })
  ).toBeVisible()
  expect(
    screen.queryByText('Model data could not be loaded')
  ).not.toBeInTheDocument()
})

it('preserves healthy catalog details when legacy pricing returns HTTP 500', async () => {
  detailRoute.modelId = catalog.models[0].public_model_id
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/pricing') {
      throw new AxiosError(
        'Synthetic HTTP 500',
        undefined,
        undefined,
        undefined,
        response({ success: false }, 500)
      )
    }
    return response({ success: true, data: catalog })
  })
  const { client, redirect } = show(true)
  expect(
    await screen.findByRole('heading', { name: catalog.models[0].name })
  ).toBeVisible()
  await waitFor(() =>
    expect(client.getQueryState(['pricing'])?.status).toBe('error')
  )
  expect(redirect).not.toHaveBeenCalled()
})

it('shows a local detail error after cached legacy data fails to refresh and recovers', async () => {
  let failed = false
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/pricing' && failed) {
      throw new AxiosError(
        'Synthetic HTTP 500',
        undefined,
        undefined,
        undefined,
        response({ success: false }, 500)
      )
    }
    return response(
      url === '/api/pricing' ? pricing : { success: true, data: catalog }
    )
  })
  const { client, redirect } = show(true)
  expect(
    await screen.findByRole('heading', { name: 'retry-model' })
  ).toBeVisible()
  failed = true
  await client.invalidateQueries({ queryKey: ['pricing'] })
  expect(
    await screen.findByText('Model data could not be loaded')
  ).toBeVisible()
  expect(redirect).not.toHaveBeenCalled()
  failed = false
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(
    await screen.findByRole('heading', { name: 'retry-model' })
  ).toBeVisible()
  expect(
    screen.queryByText('Model data could not be loaded')
  ).not.toBeInTheDocument()
})
