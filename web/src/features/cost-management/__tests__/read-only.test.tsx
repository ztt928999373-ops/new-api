/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
// Copyright (c) QuantumNous and contributors.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import { AxiosError, type AxiosResponse } from 'axios'
import i18next from 'i18next'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import zhResource from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { CostManagement } from '..'
import { formatUSDmicros } from '../api'

const originalAuth = useAuthStore.getState().auth
const clients: QueryClient[] = []
const policy = {
  channel_id: 1,
  enabled: false,
  limit_micros: 123456789,
  held_micros: 1000001,
  allocated_liability_micros: 2000002,
  reconciliation_frozen: true,
  valid_until: 2000000000,
  model: 'synthetic-model',
  revision: 1,
}
const statement = {
  id: 'synthetic-statement',
  channel_id: 1,
  state: 'settled',
  alert: 'conflicting_bill',
  observed_supplier_cost_usd_micros: 1234567,
  operator_estimated_cost_usd_micros: 1000000,
  actual_minus_estimate_usd_micros: 234567,
  reserved_ceiling_usd_micros: 2000000,
  created_at: 1700000000,
}
const billResponse = {
  success: true,
  data: [statement],
  total: 1,
  page: 1,
  page_size: 50,
  verification: 'operator_attested_not_independently_verified',
  supplier_account_hard_limit: 'not_connected',
  time_basis: 'statement_recorded_at_not_supplier_usage_period',
  revenue_status: 'unknown_without_revenue_allocation',
  platform_revenue_usd_micros: null,
  gross_profit_usd_micros: null,
  user_charge_status: 'available_references_not_totals',
  user_charge_references: [
    { log_id: 1, request_id: 'synthetic-request', quota: 500000, log_type: 2 },
  ],
}

beforeEach(() => {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: { id: 100, username: 'synthetic-root', role: 100 },
    },
  })
  vi.spyOn(api, 'post').mockRejectedValue(
    new Error('Read-only page must not POST')
  )
  vi.spyOn(api, 'put').mockRejectedValue(
    new Error('Read-only page must not PUT')
  )
  vi.spyOn(api, 'delete').mockRejectedValue(
    new Error('Read-only page must not DELETE')
  )
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/channel/') {
      return {
        data: {
          success: true,
          data: {
            items: [
              {
                id: 1,
                name: 'Synthetic Alpha',
                key: 'synthetic-secret-must-not-render',
              },
              { id: 2, name: 'Synthetic Beta' },
            ],
            total: 2,
          },
        },
      }
    }
    const id = Number(url.split('/')[3])
    if (url.endsWith('/statements')) {
      return {
        data: { ...billResponse, data: [{ ...statement, channel_id: id }] },
      }
    }
    return { data: { success: true, data: { ...policy, channel_id: id } } }
  })
})
afterEach(async () => {
  expect(api.post).not.toHaveBeenCalled()
  expect(api.put).not.toHaveBeenCalled()
  expect(api.delete).not.toHaveBeenCalled()
  clients.forEach((client) => client.clear())
  clients.length = 0
  useAuthStore.setState({ auth: originalAuth })
  vi.restoreAllMocks()
  await i18next.changeLanguage('en')
})
function showPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  clients.push(client)
  return render(
    <QueryClientProvider client={client}>
      <CostManagement />
    </QueryClientProvider>
  )
}
async function chooseChannel(id = '1') {
  await screen.findByRole('option', { name: '#1 · Synthetic Alpha' })
  fireEvent.change(screen.getByLabelText('Channel'), { target: { value: id } })
}

it('shows exact internal and unverified accounting sources without key disclosure or mutation controls', async () => {
  showPage()
  await chooseChannel()
  expect(await screen.findByText('USD 123.456789')).toBeVisible()
  expect(screen.getByText('USD 2.000002')).toBeVisible()
  expect(screen.getByText('USD 1.000001')).toBeVisible()
  expect(screen.getByText('Bill anomaly freeze')).toBeVisible()
  expect(await screen.findByText('Not independently verified')).toBeVisible()
  const table = screen.getByRole('table', {
    name: 'Structured supplier statements',
  })
  expect(within(table).getByText('USD 1.234567')).toBeVisible()
  expect(within(table).getByText('USD 0.234567')).toBeVisible()
  expect(within(table).getByText('Conflicting bill evidence')).toBeVisible()
  expect(
    screen.queryByText('synthetic-secret-must-not-render')
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: /save|reconcile|unfreeze|delete/i })
  ).not.toBeInTheDocument()
  expect(
    screen.getByText(
      /Internal budget protection is not a supplier account hard limit/
    )
  ).toBeVisible()
})

it('switches channels and sends time/state/alert filters to the correct GET endpoint', async () => {
  showPage()
  await chooseChannel()
  await screen.findByText('Not independently verified')
  fireEvent.change(screen.getByLabelText('Review state'), {
    target: { value: 'settled' },
  })
  fireEvent.change(screen.getByLabelText('Statement alerts'), {
    target: { value: 'true' },
  })
  fireEvent.change(screen.getByLabelText('Recorded from'), {
    target: { value: '2026-01-01' },
  })
  fireEvent.change(screen.getByLabelText('Recorded through'), {
    target: { value: '2026-01-02' },
  })
  await waitFor(() =>
    expect(api.get).toHaveBeenCalledWith(
      '/api/channel/1/upstream-budget/statements',
      expect.objectContaining({
        params: expect.objectContaining({
          state: 'settled',
          alerts_only: true,
          start: expect.any(Number),
          end: expect.any(Number),
          page: 1,
        }),
      })
    )
  )
  fireEvent.change(screen.getByLabelText('Channel'), { target: { value: '2' } })
  await waitFor(() =>
    expect(api.get).toHaveBeenCalledWith(
      '/api/channel/2/upstream-budget/statements',
      expect.any(Object)
    )
  )
  fireEvent.change(screen.getByLabelText('Recorded from'), {
    target: { value: '2026-02-01' },
  })
  expect(await screen.findByText('Invalid statement date range')).toBeVisible()
  expect(
    screen.queryByRole('table', { name: 'Structured supplier statements' })
  ).not.toBeInTheDocument()
})

it('does not fetch cost data for an administrator without the existing permission', async () => {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: { id: 10, username: 'synthetic-admin', role: 10 },
    },
  })
  showPage()
  expect(await screen.findByText('Insufficient cost permissions')).toBeVisible()
  expect(api.get).not.toHaveBeenCalled()
})

it('shows missing budgets and empty statements without inventing zero balances', async () => {
  vi.mocked(api.get).mockImplementation(async (url) => {
    if (url === '/api/channel/') {
      return {
        data: {
          success: true,
          data: { items: [{ id: 1, name: 'Synthetic Alpha' }], total: 1 },
        },
      }
    }
    if (url.endsWith('/statements')) {
      return {
        data: {
          ...billResponse,
          data: [],
          total: 0,
          user_charge_references: [],
        },
      }
    }
    throw new AxiosError('missing', undefined, undefined, undefined, {
      status: 404,
      data: { code: 'UPSTREAM_BUDGET_NOT_CONFIGURED' },
    } as AxiosResponse)
  })
  showPage()
  await chooseChannel()
  expect(await screen.findByText('No budget configured')).toBeVisible()
  expect(
    await screen.findByText('No statements match these filters')
  ).toBeVisible()
  expect(screen.queryByText('USD 0.000000')).not.toBeInTheDocument()
})

it('shows failed or incompatible responses as unavailable and hides previously loaded cost cards', async () => {
  showPage()
  await chooseChannel()
  await screen.findByText('USD 123.456789')
  vi.mocked(api.get).mockRejectedValue(
    new AxiosError('unavailable', undefined, undefined, undefined, {
      status: 503,
    } as AxiosResponse)
  )
  fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  expect(await screen.findByText('Cost service unavailable')).toBeVisible()
  expect(screen.queryByText('USD 123.456789')).not.toBeInTheDocument()
})

it('preserves six decimal micro-units and rejects unsafe display integers', () => {
  expect(formatUSDmicros(1)).toBe('USD 0.000001')
  expect(formatUSDmicros(-1)).toBe('USD -0.000001')
  expect(formatUSDmicros(9000000000000000)).toBe('USD 9000000000.000000')
  expect(formatUSDmicros(Number.MAX_SAFE_INTEGER + 1)).toBe('—')
  expect(formatUSDmicros(null)).toBe('—')
})

it('uses the real Chinese locale resource after language switching without changing USD amounts', async () => {
  showPage()
  await chooseChannel()
  await screen.findByText('USD 123.456789')
  i18next.addResourceBundle(
    'zhCN',
    'translation',
    zhResource.translation,
    true,
    true
  )
  await act(async () => {
    await i18next.changeLanguage('zhCN')
  })
  expect(screen.getByRole('heading', { name: '费用管理' })).toBeVisible()
  expect(screen.getByText('平台内部预算上限')).toBeVisible()
  expect(screen.getByText('USD 123.456789')).toBeVisible()
})

it('shows no-channel and loading states without inventing a budget', async () => {
  let finish: ((value: { data: unknown }) => void) | undefined
  vi.mocked(api.get).mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve
      })
  )
  showPage()
  expect(screen.getByText('Loading...')).toBeVisible()
  await waitFor(() => expect(finish).toBeDefined())
  finish?.({ data: { success: true, data: { items: [], total: 0 } } })
  expect(await screen.findByText('No channels available')).toBeVisible()
  expect(screen.queryByText(/USD /)).not.toBeInTheDocument()
})

it('reports server permission denial even when local permissions are stale', async () => {
  showPage()
  await screen.findByRole('option', { name: '#1 · Synthetic Alpha' })
  vi.mocked(api.get).mockRejectedValue(
    new AxiosError('denied', undefined, undefined, undefined, {
      status: 403,
    } as AxiosResponse)
  )
  fireEvent.change(screen.getByLabelText('Channel'), { target: { value: '1' } })
  expect(await screen.findByText('Insufficient cost permissions')).toBeVisible()
  expect(screen.queryByText('USD 123.456789')).not.toBeInTheDocument()
})

it('rejects a wrong-channel budget instead of displaying stale financial data', async () => {
  showPage()
  await chooseChannel()
  await screen.findByText('USD 123.456789')
  vi.mocked(api.get).mockImplementation(async (url) => {
    if (url.endsWith('/statements')) {
      return {
        data: { ...billResponse, data: [{ ...statement, channel_id: 2 }] },
      }
    }
    return { data: { success: true, data: policy } }
  })
  fireEvent.change(screen.getByLabelText('Channel'), { target: { value: '2' } })
  expect(await screen.findByText('Cost service unavailable')).toBeVisible()
  expect(screen.queryByText('USD 123.456789')).not.toBeInTheDocument()
})

it('rejects unsafe financial API numbers without rounding them or showing zero', async () => {
  showPage()
  await screen.findByRole('option', { name: '#1 · Synthetic Alpha' })
  vi.mocked(api.get).mockImplementation(async (url) => {
    if (url.endsWith('/statements')) {
      return { data: billResponse }
    }
    return {
      data: {
        success: true,
        data: { ...policy, held_micros: Number.MAX_SAFE_INTEGER + 1 },
      },
    }
  })
  fireEvent.change(screen.getByLabelText('Channel'), { target: { value: '1' } })
  expect(await screen.findByText('Cost service unavailable')).toBeVisible()
  expect(screen.queryByText('USD 0.000000')).not.toBeInTheDocument()
})

it('pages statements on the server and resets to the first page when filters change', async () => {
  const original = vi.mocked(api.get).getMockImplementation()
  if (!original) {
    throw new Error('Network fixture required')
  }
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url.endsWith('/statements')) {
      return {
        data: {
          ...billResponse,
          total: 51,
          page: (config?.params as { page?: number } | undefined)?.page ?? 1,
        },
      }
    }
    return original(url, config)
  })
  showPage()
  await chooseChannel()
  await screen.findByText('Not independently verified')
  expect(
    screen.getByRole('button', { name: 'Previous statements' })
  ).toBeDisabled()
  fireEvent.click(screen.getByRole('button', { name: 'Next statements' }))
  await waitFor(() =>
    expect(api.get).toHaveBeenCalledWith(
      '/api/channel/1/upstream-budget/statements',
      expect.objectContaining({ params: expect.objectContaining({ page: 2 }) })
    )
  )
  await screen.findByRole('table', { name: 'Structured supplier statements' })
  expect(screen.getByRole('button', { name: 'Next statements' })).toBeDisabled()
  fireEvent.change(screen.getByLabelText('Statement alerts'), {
    target: { value: 'true' },
  })
  await waitFor(() =>
    expect(api.get).toHaveBeenCalledWith(
      '/api/channel/1/upstream-budget/statements',
      expect.objectContaining({
        params: expect.objectContaining({ page: 1, alerts_only: true }),
      })
    )
  )
})
