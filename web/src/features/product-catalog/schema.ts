// Copyright (c) QuantumNous and contributors.
import { z } from 'zod'

const text = z.string()
const strings = z.array(text)
const nullableText = text.nullable()
const decimal = z
  .string()
  .regex(/^(0|[1-9][0-9]*)(\.[0-9]+)?$/)
  .nullable()
const prices = z.object({
  input: decimal,
  output: decimal,
  cache_read: decimal,
  cache_write: decimal,
})
const limit = z.number().int().positive().nullable()
const verification = z
  .object({
    state: text.refine((v) =>
      [
        'unverified',
        'mock_passed',
        'supplier_verified',
        'partial',
        'unsupported',
      ].includes(v)
    ),
    kind: nullableText,
    evidence_id: nullableText,
    tested_on: nullableText,
    notes: text,
  })
  .superRefine((v, ctx) => {
    const issue = () =>
      ctx.addIssue({ code: 'custom', message: 'Invalid verification evidence' })
    if (!v.notes.trim()) issue()
    if (v.state === 'unverified') {
      if (v.kind !== null || v.evidence_id !== null || v.tested_on !== null) {
        issue()
      }
      return
    }
    if (
      v.state === 'unsupported' &&
      v.kind === null &&
      v.evidence_id === null &&
      v.tested_on === null
    ) {
      return
    }
    if (
      !['mock', 'supplier'].includes(v.kind ?? '') ||
      !v.evidence_id ||
      !/^[a-zA-Z0-9][a-zA-Z0-9._-]*$/.test(v.evidence_id) ||
      !z.iso.date().safeParse(v.tested_on).success
    ) {
      issue()
    }
    if (
      (v.state === 'supplier_verified' && v.kind !== 'supplier') ||
      (v.state === 'mock_passed' && v.kind !== 'mock')
    ) {
      issue()
    }
  })
const features = z.object({
  text_input: verification,
  text_output: verification,
  image_input: verification,
  image_output: verification,
  tool_calling: verification,
  streaming: verification,
  cache_read: verification,
  cache_write: verification,
})
const model = z
  .object({
    public_model_id: text.min(1),
    name: text.min(1),
    manufacturer: text.min(1),
    manufacturer_version: z.object({ id: text, kind: text }),
    sources: z.array(
      z.object({
        id: text,
        url: z.string().url().startsWith('https://'),
        checked_on: text,
        official_updated_on: nullableText,
      })
    ),
    manufacturer_facts: z.object({
      protocols: strings,
      input_modalities: strings,
      output_modalities: strings,
      limits: z.object({ context_tokens: limit, max_output_tokens: limit }),
      capabilities: z.object({
        image_input: z.boolean().nullable(),
        image_output: z.boolean().nullable(),
        tool_calling: z.boolean().nullable(),
        streaming: z.boolean().nullable(),
        cache_read: z.boolean().nullable(),
        cache_write: z.boolean().nullable(),
      }),
      source_ids: strings,
      notes: strings,
    }),
    official_reference_prices: z.object({
      price_type: text,
      currency: text,
      unit: text,
      checked_on: text,
      official_updated_on: nullableText,
      source_ids: strings,
      quotes: z.array(
        z.object({
          id: text,
          effective_from: nullableText,
          effective_until: nullableText,
          cache_write_ttl_seconds: limit,
          prices,
          conditions: text,
        })
      ),
      additional_charges: z.array(
        z.object({
          effective_from: nullableText,
          effective_until: nullableText,
          kind: text,
          amount: text,
          unit: text,
          notes: text,
        })
      ),
    }),
    platform_selling_prices: z
      .object({
        price_type: text.refine((value) => ['platform_selling'].includes(value)),
        state: text.refine((value) =>
          ['configured', 'unconfigured'].includes(value)
        ),
        currency: nullableText,
        unit: nullableText,
        prices,
        source: nullableText,
        pricing_version: nullableText,
        updated_on: nullableText,
        effective_from: nullableText,
        effective_until: nullableText,
      })
      .superRefine((p, ctx) => {
        const issue = () =>
          ctx.addIssue({
            code: 'custom',
            message: 'Invalid selling price configuration',
          })
        if (p.state === 'unconfigured') {
          if (
            [
              p.currency,
              p.unit,
              p.source,
              p.pricing_version,
              p.updated_on,
              p.effective_from,
              p.effective_until,
              ...Object.values(p.prices),
            ].some((value) => value !== null)
          ) {
            issue()
          }
          return
        }
        if (
          !p.currency ||
          !/^[A-Z]{3}$/.test(p.currency) ||
          p.unit !== 'per_1m_tokens' ||
          p.source !== 'new_api_effective_pricing' ||
          !p.pricing_version ||
          !z.iso.date().safeParse(p.updated_on).success
        ) {
          issue()
        }
        if (
          [p.effective_from, p.effective_until].some(
            (date) => date !== null && !z.iso.date().safeParse(date).success
          )
        ) {
          issue()
        }
        if (
          p.effective_from !== null &&
          p.effective_until !== null &&
          p.effective_from >= p.effective_until
        ) {
          issue()
        }
      }),
    platform_support: z.object({ overall: verification, features }),
    supply: z.object({
      mode: text.refine((v) =>
        ['unassigned', 'official_direct', 'approved_aggregator'].includes(v)
      ),
      supplier_name: nullableText,
    }),
    launch_input_modalities: strings,
    launch_output_modalities: strings,
  })
  .superRefine((m, ctx) => {
    // Selling-price configuration does not imply assignment or live verification.
    // Public responses cannot prove private route bindings; the server checks those.
    const issue = () =>
      ctx.addIssue({
        code: 'custom',
        message: 'Inconsistent supplier verification',
      })
    if (m.supply.mode === 'unassigned' && m.supply.supplier_name !== null) {
      issue()
    }
    const states = [
      m.platform_support.overall,
      ...Object.values(m.platform_support.features),
    ]
    if (
      m.supply.mode === 'unassigned' &&
      states.some((v) => v.state === 'supplier_verified')
    ) {
      issue()
    }
    if (
      m.platform_support.overall.state === 'supplier_verified' &&
      Object.values(m.platform_support.features).some(
        (v) => v.state !== 'supplier_verified'
      )
    ) {
      issue()
    }
  })
export const catalogResponseSchema = z.object({
  success: z.literal(true),
  data: z.object({
    schema_version: z.number().refine((v) => v === 1),
    version: text,
    verified_on: text,
    models: z.array(model),
  }),
})
export type Catalog = z.infer<typeof catalogResponseSchema>['data']
export type CatalogModel = Catalog['models'][number]
