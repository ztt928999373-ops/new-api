// Copyright (c) QuantumNous and contributors.
import { isAxiosError } from 'axios'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'

export function CatalogRequestError(props: {
  error: unknown
  onRetry: () => void
}) {
  const { t } = useTranslation()
  const unavailable =
    props.error instanceof TypeError ||
    (isAxiosError(props.error) &&
      (!props.error.response ||
        [502, 503, 504].includes(props.error.response.status)))
  return (
    <ErrorState
      title={t(
        unavailable
          ? 'Service temporarily unavailable'
          : 'Model data could not be loaded'
      )}
      description={t(
        unavailable
          ? 'The service could not be reached. Please retry.'
          : 'Please retry loading the model data.'
      )}
      onRetry={props.onRetry}
    />
  )
}
