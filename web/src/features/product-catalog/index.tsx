// Copyright (c) QuantumNous and contributors.
import { Link, useParams } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { EmptyState as CatalogEmptyState } from '@/components/empty-state'
import { PublicLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { EmptyState } from '@/features/pricing/components/empty-state'
import { ModelDetails } from '@/features/pricing/components/model-details'
import { SearchBar } from '@/features/pricing/components/search-bar'
import { usePricingData } from '@/features/pricing/hooks/use-pricing-data'

import { useProductCatalog } from './api'
import { CatalogFacts } from './facts'
import { filterCatalogModels } from './filters'
import { CatalogRequestError } from './request-error'

export function CatalogMarketplace() {
  const { t } = useTranslation()
  const query = useProductCatalog()
  const [search, setSearch] = useState(
    () => sessionStorage.getItem('catalog-search') ?? ''
  )
  const [capability, setCapability] = useState<string | null>(() =>
    sessionStorage.getItem('catalog-capability')
  )
  useEffect(() => {
    sessionStorage.setItem('catalog-search', search)
    if (capability) sessionStorage.setItem('catalog-capability', capability)
    else sessionStorage.removeItem('catalog-capability')
  }, [search, capability])
  if (query.isPending) return <LoadingState />
  if (query.isError)
    {return (
      <CatalogRequestError
        error={query.error}
        onRetry={() => {
          void query.refetch()
        }}
      />
    )}
  if (query.data.models.length === 0)
    {return (
      <CatalogEmptyState
        title={t('No catalog models available')}
        description={t(
          'The catalog is empty. No model availability is implied.'
        )}
        action={
          <Button
            variant='outline'
            onClick={() => {
              void query.refetch()
            }}
          >
            {t('Retry')}
          </Button>
        }
      />
    )}
  const models = filterCatalogModels(query.data.models, search, capability)
  return (
    <section className='mb-8 space-y-4' aria-label={t('Launch model catalog')}>
      <h2 className='text-2xl font-semibold'>{t('Launch model catalog')}</h2>
      <p>
        {t('Catalog version')}: {query.data.version} · {query.data.verified_on}
      </p>
      <p>
        {t('Public catalog models')}: {query.data.models.length} ·{' '}
        {t('Models with configured suppliers')}:{' '}
        {query.data.models.filter((m) => m.supply.mode !== 'unassigned').length}{' '}
        · {t('Callable catalog models')}: {t('Pending verification')}
      </p>
      <p>{t('Callability requires live supplier verification.')}</p>
      <SearchBar
        value={search}
        onChange={setSearch}
        onClear={() => setSearch('')}
      />
      <div className='flex flex-wrap gap-2'>
        {[
          'image_input',
          'tool_calling',
          'streaming',
          'cache_read',
          'cache_write',
        ].map((key) => (
          <Button
            key={key}
            variant={capability === key ? 'default' : 'outline'}
            aria-pressed={capability === key}
            onClick={() => setCapability(capability === key ? null : key)}
          >
            {t(key)}
          </Button>
        ))}
      </div>
      {models.length === 0 && (
        <EmptyState
          searchQuery={search}
          hasActiveFilters={!!capability}
          onClearFilters={() => {
            setSearch('')
            setCapability(null)
          }}
        />
      )}
      <div className='grid gap-4 lg:grid-cols-2'>
        {models.map((m) => (
          <Card key={m.public_model_id}>
            <CardHeader>
              <CardTitle>
                <Link
                  to='/pricing/$modelId'
                  params={{ modelId: m.public_model_id }}
                >
                  {m.name}
                </Link>
              </CardTitle>
              <p>
                {m.public_model_id} <CopyButton value={m.public_model_id} />
              </p>
            </CardHeader>
            <CardContent>
              <CatalogFacts model={m} />
            </CardContent>
          </Card>
        ))}
      </div>
    </section>
  )
}

export function CatalogModelDetails() {
  const { t } = useTranslation()
  const { modelId } = useParams({ from: '/pricing/$modelId/' })
  const query = useProductCatalog()
  const legacy = usePricingData()
  if (
    !legacy.error &&
    legacy.models.some((m) => m.model_name === modelId) &&
    (query.isError ||
      !query.data?.models.some((m) => m.public_model_id === modelId))
  )
    {return <ModelDetails />}
  if (query.isPending) {
    return (
      <PublicLayout>
        <LoadingState />
      </PublicLayout>
    )
  }
  if (query.isError) {
    if (legacy.isLoading)
      {return (
        <PublicLayout>
          <LoadingState />
        </PublicLayout>
      )}
    return (
      <PublicLayout>
        <CatalogRequestError
          error={query.error}
          onRetry={() => {
            void query.refetch()
            void legacy.refetch()
          }}
        />
      </PublicLayout>
    )
  }
  const model = query.data.models.find((m) => m.public_model_id === modelId)
  if (!model && legacy.isLoading)
    {return (
      <PublicLayout>
        <LoadingState />
      </PublicLayout>
    )}
  if (!model && legacy.error)
    {return (
      <PublicLayout>
        <CatalogRequestError
          error={legacy.error}
          onRetry={() => {
            void legacy.refetch()
          }}
        />
      </PublicLayout>
    )}
  if (!model) return <ModelDetails />
  return (
    <PublicLayout>
      <main className='mx-auto max-w-4xl space-y-6 px-4 py-20'>
        <Link to='/pricing'>{t('Back to models')}</Link>
        <h1 className='text-3xl font-bold'>{model.name}</h1>
        <p>
          {model.public_model_id} <CopyButton value={model.public_model_id} />
        </p>
        <p>
          {t('Manufacturer version')}: {model.manufacturer_version.id} (
          {model.manufacturer_version.kind})
        </p>
        <CatalogFacts model={model} detailed />
      </main>
    </PublicLayout>
  )
}
