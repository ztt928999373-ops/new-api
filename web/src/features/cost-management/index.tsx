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
import { useQuery } from '@tanstack/react-query'
import { isAxiosError } from 'axios'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { DatePicker } from '@/components/date-picker'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { toIntlLocale } from '@/i18n/languages'
import { hasPermission } from '@/lib/admin-permissions'
import dayjs from '@/lib/dayjs'
import { formatNumber } from '@/lib/format'
import { useAuthStore } from '@/stores/auth-store'

import { readBudget, readCostChannels, readStatements } from './api'
import { BudgetSummary, StatementList } from './sections'

export function CostManagement() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const user = useAuthStore((state) => state.auth.user)
  const allowed = hasPermission(user, 'channel', 'sensitive_write')
  const [channelPage, setChannelPage] = useState(1)
  const [channelID, setChannelID] = useState<number | null>(null)
  const [page, setPage] = useState(1)
  const [state, setState] = useState('')
  const [alertsOnly, setAlertsOnly] = useState('')
  const [startDate, setStartDate] = useState('')
  const [endDate, setEndDate] = useState('')
  const start = startDate ? dayjs(startDate).startOf('day').unix() : undefined
  const end = endDate
    ? dayjs(endDate).add(1, 'day').startOf('day').unix()
    : undefined
  const invalidRange =
    (start !== undefined && !Number.isFinite(start)) ||
    (end !== undefined && !Number.isFinite(end)) ||
    (start !== undefined && end !== undefined && start >= end)
  const channels = useQuery({
    queryKey: ['cost-management', user?.id, 'channels', channelPage],
    queryFn: ({ signal }) => readCostChannels(channelPage, signal),
    enabled: allowed,
    retry: false,
    meta: { errorToast: false },
  })
  const budget = useQuery({
    queryKey: ['cost-management', user?.id, 'budget', channelID],
    queryFn: ({ signal }) => {
      if (channelID === null) {
        throw new Error('Channel selection required')
      }
      return readBudget(channelID, signal)
    },
    enabled: allowed && channelID !== null,
    retry: false,
    meta: { errorToast: false },
  })
  const filters = {
    page,
    state,
    alerts_only: alertsOnly === 'true',
    start,
    end,
  }
  const bills = useQuery({
    queryKey: ['cost-management', user?.id, 'statements', channelID, filters],
    queryFn: ({ signal }) => {
      if (channelID === null) {
        throw new Error('Channel selection required')
      }
      return readStatements(channelID, filters, signal)
    },
    enabled: allowed && channelID !== null && !invalidRange,
    retry: false,
    meta: { errorToast: false },
  })
  const error = channels.error || budget.error || bills.error
  const denied = isAxiosError(error) && error.response?.status === 403
  let budgetView: ReactNode = null
  if (budget.isLoading) {
    budgetView = <LoadingState />
  } else if (budget.data === null) {
    budgetView = (
      <Alert>
        <AlertTitle>{t('No budget configured')}</AlertTitle>
        <AlertDescription>
          {t(
            'No budget record is available for this channel. This does not mean supplier costs are zero.'
          )}
        </AlertDescription>
      </Alert>
    )
  } else if (budget.data) {
    budgetView = <BudgetSummary budget={budget.data} />
  }
  let statementView: ReactNode = null
  if (invalidRange) {
    statementView = (
      <Alert variant='destructive'>
        <AlertTitle>{t('Invalid statement date range')}</AlertTitle>
      </Alert>
    )
  } else if (bills.isLoading) {
    statementView = <LoadingState />
  } else if (bills.data) {
    statementView = (
      <StatementList
        response={bills.data}
        page={page}
        onPage={setPage}
        fetching={bills.isFetching}
      />
    )
  }
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Cost management')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button
          variant='outline'
          disabled={
            !allowed ||
            channels.isFetching ||
            budget.isFetching ||
            bills.isFetching
          }
          onClick={() => {
            void channels.refetch()
            if (channelID !== null) {
              void budget.refetch()
              if (!invalidRange) void bills.refetch()
            }
          }}
        >
          {t('Refresh')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='space-y-5 pb-6'>
          <Alert>
            <AlertTitle>{t('Read-only cost oversight')}</AlertTitle>
            <AlertDescription>
              {t(
                'Internal budget protection is not a supplier account hard limit. No supplier account budget is connected. This page cannot modify budgets, reconcile bills or unfreeze channels.'
              )}
            </AlertDescription>
          </Alert>
          {!allowed ? (
            <ErrorState
              title={t('Insufficient cost permissions')}
              description={t(
                'Cost APIs require the existing channel sensitive-write permission. This read-only page does not grant it.'
              )}
            />
          ) : (
            <>
              {channels.isLoading && <LoadingState />}
              {error && (
                <ErrorState
                  title={t(
                    denied
                      ? 'Insufficient cost permissions'
                      : 'Cost service unavailable'
                  )}
                  description={t(
                    'No failed or unavailable response is shown as zero cost. Reload or contact an authorized administrator.'
                  )}
                  onRetry={() => {
                    void channels.refetch()
                    if (channelID !== null) {
                      void budget.refetch()
                      if (!invalidRange) void bills.refetch()
                    }
                  }}
                />
              )}
              {channels.data && (
                <div className='flex flex-wrap items-end gap-3'>
                  <div className='space-y-1'>
                    <Label htmlFor='cost-channel'>{t('Channel')}</Label>
                    <NativeSelect
                      id='cost-channel'
                      value={channelID ?? ''}
                      onChange={(event) => {
                        setChannelID(
                          event.target.value ? Number(event.target.value) : null
                        )
                        setPage(1)
                      }}
                    >
                      <NativeSelectOption value=''>
                        {t('Select a channel')}
                      </NativeSelectOption>
                      {channels.data.items.map((channel) => (
                        <NativeSelectOption key={channel.id} value={channel.id}>
                          #{channel.id} · {channel.name}
                        </NativeSelectOption>
                      ))}
                    </NativeSelect>
                  </div>
                  <Button
                    variant='outline'
                    disabled={channelPage <= 1 || channels.isFetching}
                    onClick={() => {
                      setChannelPage(channelPage - 1)
                      setChannelID(null)
                      setPage(1)
                    }}
                  >
                    {t('Previous channels')}
                  </Button>
                  <Button
                    variant='outline'
                    disabled={
                      channelPage * 50 >= channels.data.total ||
                      channels.isFetching
                    }
                    onClick={() => {
                      setChannelPage(channelPage + 1)
                      setChannelID(null)
                      setPage(1)
                    }}
                  >
                    {t('Next channels')}
                  </Button>
                  <span className='text-muted-foreground text-sm'>
                    {t('Channels')}: {formatNumber(channels.data.total, locale)}
                  </span>
                </div>
              )}
              {channels.data?.total === 0 && (
                <EmptyState title={t('No channels available')} />
              )}
              {channelID === null &&
                channels.data &&
                channels.data.total > 0 && (
                  <EmptyState
                    title={t('Select a channel to inspect its costs')}
                  />
                )}
              {channelID !== null && !error && (
                <>
                  {budgetView}
                  <section
                    className='space-y-3'
                    aria-label={t('Supplier statement filters')}
                  >
                    <h3 className='text-base font-semibold'>
                      {t('Structured supplier statements')}
                    </h3>
                    <p className='text-muted-foreground text-sm'>
                      {t(
                        'Operator-attested observations are not independently verified supplier bills. Date filters apply to statement record time, not the supplier usage period. Budget totals above always show the current lifetime state.'
                      )}
                    </p>
                    <div className='flex flex-wrap items-end gap-3'>
                      <div className='space-y-1'>
                        <Label htmlFor='cost-start'>{t('Recorded from')}</Label>
                        <DatePicker
                          id='cost-start'
                          aria-label={t('Recorded from')}
                          selected={
                            startDate ? dayjs(startDate).toDate() : undefined
                          }
                          onSelect={(date) => {
                            setStartDate(
                              date ? dayjs(date).format('YYYY-MM-DD') : ''
                            )
                            setPage(1)
                          }}
                        />
                      </div>
                      <div className='space-y-1'>
                        <Label htmlFor='cost-end'>
                          {t('Recorded through')}
                        </Label>
                        <DatePicker
                          id='cost-end'
                          aria-label={t('Recorded through')}
                          selected={
                            endDate ? dayjs(endDate).toDate() : undefined
                          }
                          onSelect={(date) => {
                            setEndDate(
                              date ? dayjs(date).format('YYYY-MM-DD') : ''
                            )
                            setPage(1)
                          }}
                        />
                      </div>
                      <div className='space-y-1'>
                        <Label htmlFor='cost-state'>{t('Review state')}</Label>
                        <NativeSelect
                          id='cost-state'
                          value={state}
                          onChange={(event) => {
                            setState(event.target.value)
                            setPage(1)
                          }}
                        >
                          <NativeSelectOption value=''>
                            {t('All states')}
                          </NativeSelectOption>
                          {['pending', 'in_flight', 'unmatched', 'settled'].map(
                            (value) => (
                              <NativeSelectOption key={value} value={value}>
                                {t(value)}
                              </NativeSelectOption>
                            )
                          )}
                        </NativeSelect>
                      </div>
                      <div className='space-y-1'>
                        <Label htmlFor='cost-alert'>
                          {t('Statement alerts')}
                        </Label>
                        <NativeSelect
                          id='cost-alert'
                          value={alertsOnly}
                          onChange={(event) => {
                            setAlertsOnly(event.target.value)
                            setPage(1)
                          }}
                        >
                          <NativeSelectOption value=''>
                            {t('All statements')}
                          </NativeSelectOption>
                          <NativeSelectOption value='true'>
                            {t('Alerts only')}
                          </NativeSelectOption>
                        </NativeSelect>
                      </div>
                    </div>
                    {statementView}
                  </section>
                </>
              )}
            </>
          )}
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
