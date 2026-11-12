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
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { toIntlLocale } from '@/i18n/languages'
import dayjs from '@/lib/dayjs'
import { formatNumber } from '@/lib/format'

import { formatUSDmicros, type Budget, type Statements } from './api'

const reviewLabels: Record<string, string> = {
  pending: 'Pending review',
  in_flight: 'Attempt still in flight',
  unmatched: 'Unmatched statement',
  settled: 'Operator settlement recorded',
}
const alertLabels: Record<string, string> = {
  reservation_not_found: 'Reservation not found',
  attempt_not_finished: 'Attempt not finished',
  duplicate_reservation: 'Duplicate reservation claim',
  settlement_conflict: 'Settlement conflict',
  conflicting_bill: 'Conflicting bill evidence',
  above_reserved_ceiling: 'Above reserved ceiling',
  estimate_missing: 'Estimate missing',
  above_estimate: 'Above operator estimate',
}

export function BudgetSummary(props: { budget: Budget }) {
  const { t } = useTranslation()
  const budget = props.budget
  const expired = budget.valid_until <= Date.now() / 1000
  return (
    <section className='space-y-3' aria-label={t('Current channel budget')}>
      <div className='flex flex-wrap items-center gap-2'>
        <h3 className='font-semibold'>{t('Current channel budget')}</h3>
        <Badge
          variant={
            budget.enabled && !expired && !budget.reconciliation_frozen
              ? 'secondary'
              : 'destructive'
          }
        >
          {t(
            budget.enabled
              ? 'Budget admission enabled'
              : 'Budget admission frozen'
          )}
        </Badge>
        {budget.reconciliation_frozen && (
          <Badge variant='destructive'>{t('Bill anomaly freeze')}</Badge>
        )}
        {expired && <Badge variant='destructive'>{t('Budget expired')}</Badge>}
        <span className='text-muted-foreground text-sm'>
          {t('Valid until')}:{' '}
          {dayjs.unix(budget.valid_until).format('YYYY-MM-DD HH:mm:ss')}
        </span>
      </div>
      <div className='grid gap-3 sm:grid-cols-3'>
        {(
          [
            ['Internal budget limit', budget.limit_micros],
            [
              'Conservative allocated liability',
              budget.allocated_liability_micros,
            ],
            ['In-flight cost reservation', budget.held_micros],
          ] as const
        ).map(([label, value]) => (
          <Card key={label}>
            <CardHeader>
              <CardTitle className='text-muted-foreground text-sm'>
                {t(label)}
              </CardTitle>
            </CardHeader>
            <CardContent>
              <p className='font-mono text-xl tabular-nums'>
                {formatUSDmicros(value)}
              </p>
            </CardContent>
          </Card>
        ))}
      </div>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Allocated liability is a conservative internal cost ceiling, not a measured supplier invoice or user wallet charge.'
        )}
      </p>
      {budget.reconciliation_frozen && (
        <Alert variant='destructive'>
          <AlertTitle>{t('Bill review required')}</AlertTitle>
          <AlertDescription>
            {t(
              'A bill anomaly has frozen cost admission. Reading this page does not clear the freeze or release existing cost responsibility.'
            )}
          </AlertDescription>
        </Alert>
      )}
    </section>
  )
}

export function StatementList(props: {
  response: Statements
  page: number
  onPage: (page: number) => void
  fetching: boolean
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  return (
    <div className='space-y-4'>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <Badge variant='outline'>{t('Not independently verified')}</Badge>
        <span className='text-muted-foreground text-sm'>
          {t('Matching statement records')}:{' '}
          {formatNumber(props.response.total, locale)}
        </span>
      </div>
      <StaticDataTable
        data={props.response.data}
        getRowKey={(row) => row.id}
        tableProps={{ 'aria-label': t('Structured supplier statements') }}
        emptyContent={
          <EmptyState title={t('No statements match these filters')} />
        }
        columns={[
          {
            id: 'recorded',
            header: t('Recorded at'),
            cell: (row) =>
              dayjs.unix(row.created_at).format('YYYY-MM-DD HH:mm:ss'),
          },
          {
            id: 'observed',
            header: t('Unverified supplier observation'),
            cell: (row) =>
              formatUSDmicros(row.observed_supplier_cost_usd_micros),
          },
          {
            id: 'estimated',
            header: t('Operator cost estimate'),
            cell: (row) =>
              formatUSDmicros(row.operator_estimated_cost_usd_micros),
          },
          {
            id: 'delta',
            header: t('Observation minus estimate'),
            cell: (row) =>
              formatUSDmicros(row.actual_minus_estimate_usd_micros),
          },
          {
            id: 'review',
            header: t('Review state'),
            cell: (row) => (
              <Badge variant='outline'>
                {t(reviewLabels[row.state] ?? 'Unknown review state')}
              </Badge>
            ),
          },
          {
            id: 'alert',
            header: t('Anomaly reason'),
            cell: (row) =>
              row.alert ? (
                <Badge variant='destructive'>
                  {t(alertLabels[row.alert] ?? 'Unknown statement anomaly')}
                </Badge>
              ) : (
                t('No recorded anomaly')
              ),
          },
        ]}
      />
      <div className='flex flex-wrap items-center gap-3'>
        <Button
          variant='outline'
          disabled={props.page <= 1 || props.fetching}
          onClick={() => props.onPage(props.page - 1)}
        >
          {t('Previous statements')}
        </Button>
        <span>
          {t('Page')} {formatNumber(props.page, locale)}
        </span>
        <Button
          variant='outline'
          disabled={props.page * 50 >= props.response.total || props.fetching}
          onClick={() => props.onPage(props.page + 1)}
        >
          {t('Next statements')}
        </Button>
      </div>
      <Alert>
        <AlertTitle>{t('Separate accounting sources')}</AlertTitle>
        <AlertDescription>
          {t(
            'Independently verified supplier costs are unavailable. Platform revenue and gross profit cannot be inferred from wallet quota or these operator-attested statements. No aggregate bill is allocated to individual requests here.'
          )}
        </AlertDescription>
      </Alert>
      <section
        className='space-y-2'
        aria-label={t('User internal charge references')}
      >
        <h4 className='font-semibold'>
          {t('User internal charge references')}
        </h4>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Quota references belong to the current statement page only, are not revenue totals and must not be summed across paid retries or pages.'
          )}
        </p>
        {props.response.user_charge_status === 'unavailable' ? (
          <Alert variant='destructive'>
            <AlertTitle>{t('User charge references unavailable')}</AlertTitle>
          </Alert>
        ) : (
          <>
            {props.response.user_charge_status ===
              'truncated_references_not_totals' && (
              <p>{t('User charge references are truncated')}</p>
            )}
            <StaticDataTable
              data={props.response.user_charge_references}
              getRowKey={(row) => row.log_id}
              tableProps={{
                'aria-label': t('User internal charge references'),
              }}
              emptyContent={t('No charge references on this statement page')}
              columns={[
                {
                  id: 'log',
                  header: t('Log ID'),
                  cell: (row) => formatNumber(row.log_id, locale),
                },
                {
                  id: 'quota',
                  header: t('Internal quota units'),
                  cell: (row) => formatNumber(row.quota, locale),
                },
                {
                  id: 'type',
                  header: t('Log type'),
                  cell: (row) =>
                    t(
                      row.log_type === 2
                        ? 'Consumption reference'
                        : 'Refund reference'
                    ),
                },
              ]}
            />
          </>
        )}
      </section>
    </div>
  )
}
