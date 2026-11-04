// Copyright (c) QuantumNous and contributors.
import { useTranslation } from 'react-i18next'

import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import type { CatalogModel } from './api'

export function CatalogFacts(props: {
  model: CatalogModel
  detailed?: boolean
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const m = props.model
  const unknown = t('Pending verification')
  return (
    <div className='space-y-4 text-sm'>
      <p>
        {t('Manufacturer')}: {m.manufacturer} · {t('Text model')}
      </p>
      <p>
        {t('Launch scope')}: {m.launch_input_modalities.join(', ')} →{' '}
        {m.launch_output_modalities.join(', ')}
      </p>
      <p>
        {t('Context tokens')}:{' '}
        {m.manufacturer_facts.limits.context_tokens == null
          ? unknown
          : formatNumber(
              m.manufacturer_facts.limits.context_tokens,
              locale
            )}{' '}
        · {t('Maximum output tokens')}:{' '}
        {m.manufacturer_facts.limits.max_output_tokens == null
          ? unknown
          : formatNumber(m.manufacturer_facts.limits.max_output_tokens, locale)}
      </p>
      <p>
        {t('Platform supplier')}: {m.supply.supplier_name ?? unknown} (
        {t(m.supply.mode)})
      </p>
      {m.supply.mode === 'unassigned' && (
        <p className='text-destructive font-medium'>
          {t('Supplier not configured. Not callable.')}
        </p>
      )}
      <p>
        {t('Platform verification')}: {t(m.platform_support.overall.state)} ·{' '}
        {t(m.platform_support.overall.notes)}
      </p>
      <p className='text-muted-foreground'>
        {t(
          'Official capabilities do not imply platform support. Reference prices are not selling prices.'
        )}
      </p>
      <h3 className='font-semibold'>{t('Official reference prices')}</h3>
      <p>
        {m.official_reference_prices.currency} /{' '}
        {m.official_reference_prices.unit} ·{' '}
        {m.official_reference_prices.checked_on}
      </p>
      {m.official_reference_prices.quotes.map((q) => (
        <div key={q.id} className='rounded border p-3'>
          <p>
            {q.id} · {t('Manufacturer reference pricing notes')}: {q.conditions}
          </p>
          <p>
            {t('Effective period')}: {q.effective_from ?? unknown} →{' '}
            {q.effective_until ?? unknown}
          </p>
          <p>
            {t('Cache TTL seconds')}: {q.cache_write_ttl_seconds ?? unknown}
          </p>
          {Object.entries(q.prices).map(([key, value]) => (
            <p key={key}>
              {t(key)}: {value ?? unknown}
            </p>
          ))}
        </div>
      ))}
      <h3 className='font-semibold'>{t('Platform selling prices')}</h3>
      <p>
        {t(m.platform_selling_prices.state)} ·{' '}
        {m.platform_selling_prices.currency ?? unknown} /{' '}
        {m.platform_selling_prices.unit ?? unknown}
      </p>
      {Object.entries(m.platform_selling_prices.prices).map(([key, value]) => (
        <p key={key}>
          {t(key)}: {value ?? unknown}
        </p>
      ))}
      {props.detailed && (
        <>
          <h3 className='font-semibold'>{t('Official capabilities')}</h3>
          <p>
            {t('Input modalities')}:{' '}
            {m.manufacturer_facts.input_modalities.join(', ')}
          </p>
          <p>
            {t('Output modalities')}:{' '}
            {m.manufacturer_facts.output_modalities.join(', ')}
          </p>
          <p>
            {t('Official protocols')}:{' '}
            {m.manufacturer_facts.protocols.join(', ')}
          </p>
          {Object.entries(m.manufacturer_facts.capabilities).map(
            ([key, value]) => (
              <p key={key}>
                {t(key)}:{' '}
                {value == null
                  ? unknown
                  : t(value ? 'Supported' : 'Not supported')}
              </p>
            )
          )}
          <h3 className='font-semibold'>{t('Platform verification')}</h3>
          {Object.entries(m.platform_support.features).map(([key, value]) => (
            <p key={key}>
              {t(key)}: {t(value.state)} · {t(value.notes)} ·{' '}
              {value.tested_on ?? unknown}
            </p>
          ))}
          {m.manufacturer_facts.notes.map((note) => (
            <p key={note}>
              {t('Original manufacturer description')}: {note}
            </p>
          ))}
          {m.official_reference_prices.additional_charges.map((charge) => (
            <p
              key={`${charge.kind}-${charge.unit}-${charge.amount}-${charge.effective_from}`}
            >
              {charge.kind}: {charge.amount} / {charge.unit} · {charge.notes}
            </p>
          ))}
        </>
      )}
      <h3 className='font-semibold'>{t('Official sources')}</h3>
      {m.sources.map((source) => (
        <p key={source.id}>
          <a
            className='underline'
            href={source.url}
            target='_blank'
            rel='noreferrer'
          >
            {source.id}
          </a>{' '}
          · {t('Checked on')}: {source.checked_on}
        </p>
      ))}
    </div>
  )
}
