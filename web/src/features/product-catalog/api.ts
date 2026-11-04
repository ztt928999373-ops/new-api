// Copyright (c) QuantumNous and contributors.
import { useQuery } from '@tanstack/react-query'

import { api } from '@/lib/api'

import { catalogResponseSchema, type Catalog } from './schema'

export type { Catalog, CatalogModel } from './schema'

export function useProductCatalog() {
  return useQuery({
    queryKey: ['product-catalog'],
    meta: { errorHandledLocally: true },
    queryFn: async (): Promise<Catalog> => {
      const response = await api.get<unknown>('/api/product-catalog')
      const parsed = catalogResponseSchema.safeParse(response.data)
      if (!parsed.success) throw new Error('Invalid model catalog response')
      return parsed.data.data
    },
    staleTime: 60_000,
  })
}
