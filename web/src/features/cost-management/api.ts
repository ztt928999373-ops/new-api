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
import { isAxiosError } from 'axios'
import { z } from 'zod'

import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

const amount = z.number().int().min(0).max(9_000_000_000_000_000)
const signedAmount = z
  .number()
  .int()
  .min(-9_000_000_000_000_000)
  .max(9_000_000_000_000_000)
const budgetSchema = z.object({
  channel_id: z.number().int().positive(),
  enabled: z.boolean(),
  limit_micros: amount,
  held_micros: amount,
  allocated_liability_micros: amount,
  reconciliation_frozen: z.boolean(),
  valid_until: z.number().int(),
  model: z.string(),
  revision: z.number().int(),
})
const statementSchema = z.object({
  id: z.string(),
  channel_id: z.number().int(),
  state: z.string(),
  alert: z.string(),
  observed_supplier_cost_usd_micros: amount,
  operator_estimated_cost_usd_micros: amount.nullable(),
  actual_minus_estimate_usd_micros: signedAmount.nullable(),
  reserved_ceiling_usd_micros: amount,
  created_at: z.number().int(),
})
const statementsSchema = z.object({
  data: z
    .array(statementSchema)
    .nullable()
    .transform((data) => data ?? []),
  total: z.number().int().nonnegative(),
  page: z.number().int().positive(),
  page_size: z.literal(50),
  verification: z.literal('operator_attested_not_independently_verified'),
  supplier_account_hard_limit: z.literal('not_connected'),
  time_basis: z.literal('statement_recorded_at_not_supplier_usage_period'),
  revenue_status: z.literal('unknown_without_revenue_allocation'),
  platform_revenue_usd_micros: z.null(),
  gross_profit_usd_micros: z.null(),
  user_charge_status: z.enum([
    'available_references_not_totals',
    'unavailable',
    'truncated_references_not_totals',
  ]),
  user_charge_references: z
    .array(
      z.object({
        log_id: z.number().int(),
        request_id: z.string(),
        quota: z.number().int().safe(),
        log_type: z.union([z.literal(2), z.literal(6)]),
      })
    )
    .nullable()
    .transform((data) => data ?? []),
})
export type Budget = z.infer<typeof budgetSchema>
export type Statement = z.infer<typeof statementSchema>
export type Statements = z.infer<typeof statementsSchema>
export type CostFilters = {
  page: number
  alerts_only?: boolean
  state?: string
  start?: number
  end?: number
}

export async function readCostChannels(page: number, signal?: AbortSignal) {
  const response = await api.get('/api/channel/', {
    params: { p: page, page_size: 50, view: 'cost_selector' },
    signal,
    disableDuplicate: true,
  })
  const data: unknown = requireServerSuccess(response.data).data
  return z
    .object({
      items: z
        .array(z.object({ id: z.number().int().positive(), name: z.string() }))
        .nullable()
        .transform((items) => items ?? []),
      total: z.number().int().nonnegative(),
    })
    .parse(data)
}

export async function readBudget(
  id: number,
  signal?: AbortSignal
): Promise<Budget | null> {
  try {
    const response = await api.get(`/api/channel/${id}/upstream-budget`, {
      signal,
      disableDuplicate: true,
    })
    const budget = budgetSchema.parse(requireServerSuccess(response.data).data)
    if (budget.channel_id !== id) {
      throw new Error('Cost response channel mismatch')
    }
    return budget
  } catch (error) {
    if (
      isAxiosError(error) &&
      error.response?.status === 404 &&
      error.response.data?.code === 'UPSTREAM_BUDGET_NOT_CONFIGURED'
    ) {
      return null
    }
    throw error
  }
}

export async function readStatements(
  id: number,
  filters: CostFilters,
  signal?: AbortSignal
): Promise<Statements> {
  const response = await api.get(
    `/api/channel/${id}/upstream-budget/statements`,
    { params: filters, signal, disableDuplicate: true }
  )
  const statements = statementsSchema.parse(requireServerSuccess(response.data))
  if (
    statements.page !== filters.page ||
    statements.data.some((row) => row.channel_id !== id)
  ) {
    throw new Error('Cost response scope mismatch')
  }
  return statements
}

// Wallet formatters apply configurable currency conversion and rounding. These
// supplier USD micro-units require exact six-decimal, non-converted display.
export function formatUSDmicros(value: number | null): string {
  if (value === null || !Number.isSafeInteger(value)) return '—'
  const integer = BigInt(value)
  const absolute = integer < 0n ? -integer : integer
  return `USD ${integer < 0n ? '-' : ''}${absolute / 1_000_000n}.${String(absolute % 1_000_000n).padStart(6, '0')}`
}
