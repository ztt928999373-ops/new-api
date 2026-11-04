import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
// Copyright (c) QuantumNous and contributors.
import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'

import { ModelDetailsApi } from '../components/model-details-api'

it('does not invent rate limits from model and group names', () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ModelDetailsApi
        model={{
          id: 1,
          model_name: 'legacy-unknown',
          model_ratio: 1,
          completion_ratio: 1,
          quota_type: 0,
          enable_groups: ['premium'],
          supported_endpoint_types: [],
        }}
        endpointMap={{}}
      />
    </QueryClientProvider>
  )
  expect(
    screen.getByText('Rate limit data has not been provided or verified.')
  ).toBeVisible()
  expect(screen.queryByRole('columnheader', { name: 'RPM' })).toBeNull()
  client.clear()
})
