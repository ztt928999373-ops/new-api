// Copyright (c) QuantumNous and contributors.
import { useRouter } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'

export function PricingServiceError() {
  const { t } = useTranslation()
  const router = useRouter()
  return (
    <ErrorState
      title={t('Service temporarily unavailable')}
      description={t('The service could not be reached. Please retry.')}
      onRetry={() => {
        void router.invalidate()
      }}
    />
  )
}
