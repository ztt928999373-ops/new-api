// Copyright (c) QuantumNous and contributors.
import type { CatalogModel } from './api'

export function filterCatalogModels(
  models: CatalogModel[],
  search: string,
  capability: string | null
): CatalogModel[] {
  return models.filter(
    (m) =>
      `${m.name} ${m.public_model_id} ${m.manufacturer} ${m.manufacturer_facts.protocols.join(' ')} text`
        .toLowerCase()
        .includes(search.trim().toLowerCase()) &&
      (!capability ||
        Object.entries(m.manufacturer_facts.capabilities).some(
          ([key, value]) => key === capability && value === true
        ))
  )
}
