# V1 launch catalog: data contract, stage 1

This package adds a display-only, source-checked catalog for New API. It does not
register an API route, change the UI, configure a provider, write a database,
or feed prices into billing. No API key, wallet, token accounting, settlement,
refund, channel weight or routing implementation is changed.

## Ownership and privacy

- `public_model_id`: the ID intended for clients. It is not a routing instruction.
- `manufacturer`: the original model developer, not necessarily the supplier.
- `manufacturer_version`: published specification provenance. OpenAI snapshots
  are recorded when offered; Google stable aliases are explicitly not immutable.
  This field does not apply an upstream mapping.
- `supply`: an explicitly approved public supplier label and supply mode. All
  launch entries are `unassigned`, with a null supplier name. No supplier has
  been selected or called by this stage.
- `Model.InternalRoutes`: server-only actual supplier ID, channel ID, actual
  internal/upstream model ID and configuration fingerprint. It is not decoded
  from the public artifact and is excluded from JSON. The separate `PublicModel`
  DTO cannot carry this data. No arbitrary supplier configuration/credential
  blob is accepted by the catalog.

`PublicView` is the public allowlist. `Load` also rejects unknown/private JSON
fields rather than silently accepting a channel configuration in the public
artifact. Errors do not echo rejected values. The caller owns the returned
objects; each embedded load allocates a fresh catalog, with no shared cache.

## Facts and validation status

`manufacturer_facts` contains source-backed original capabilities, protocols,
modalities and limits. Nullable booleans distinguish unknown from explicit
false. Token limits are nullable positive integers, not advertised platform
limits. All four products launch with text input/output only, even when the
manufacturer supports image, audio, video or PDF input.

`platform_support` records each feature independently, using exactly:

| State | Meaning |
| --- | --- |
| `unverified` | No applicable platform/channel test evidence |
| `mock_passed` | Synthetic adapter/fixture evidence only |
| `supplier_verified` | Recorded actual supplier evidence, with a server-side assignment |
| `partial` | Explicit incomplete coverage, with evidence and explanation |
| `unsupported` | Explicitly not offered/supported; never inferred from missing data |

Passing Mock evidence cannot be relabeled supplier evidence. Test records need
an evidence ID, test kind and date. Actual route fingerprints remain private.
No model-specific Mock or supplier test has been performed in this stage: all
shipped platform states remain `unverified`. This must not be displayed as a
working/verified supplier route. Later integration must invalidate evidence when
its actual route configuration changes; no such routing integration is added here.

## Prices and dates

- `official_reference_prices`: documented standard paid reference rates, in USD
  per 1,000,000 tokens. Separate input, output, cache-read and cache-write fields.
- `platform_selling_prices`: separate effective New API sale-price provenance;
  currently `unconfigured`, with all four amounts, currency/unit/source/version
  and update date null. It must not be filled by copying official reference rates.
- Amounts are nullable decimal strings. `null` means unknown/not separately
  quoted, not zero or free. An explicit `"0"` remains distinct.
- `checked_on` is the documentation verification date, not a claimed official
  price-change date. `official_updated_on` stays null when not stated.
- Quote end dates are exclusive. `ReferenceQuotesOn` selects dated documentation
  only; it performs no charge calculation. An unspecified start does not support
  historical-price claims before `checked_on`.
- Anthropic's 5-minute and 1-hour cache writes are alternatives, not additive.
  Google's cache storage uses a separate token-hour unit, not cache-write tokens.
- The directory excludes free-tier allowances, batch/flex/fast/priority rates,
  regional uplifts, hosted-tool charges and negotiated supplier terms.

## Launch support matrix

Verified against official sources on **2026-11-01**. These are catalog candidates,
not enabled channels. Actual supplier and internal routing ID are unconfigured
for every row. Platform capabilities and limits are unverified for every row.

| Public model ID | Manufacturer / published version | Documented API | Context / output limit | Manufacturer inputs → output |
| --- | --- | --- | --- | --- |
| `gpt-5.4-mini` | OpenAI / `gpt-5.4-mini-2026-03-17` | Chat Completions, Responses | 400,000 / 128,000 | text, image → text |
| `claude-sonnet-5-5` | Anthropic / `claude-sonnet-5-5` release ID | Messages | 1,000,000 / 128,000 | text, image → text |
| `gemini-3.5-flash-lite` | Google / same stable alias | generateContent, streamGenerateContent | 1,048,576 / 65,536 | text, image, audio, video, PDF → text |
| `gemini-3.8-flash` | Google / same stable alias | generateContent, streamGenerateContent | 1,048,576 / 65,536 | text, image, audio, video, PDF → text |

All four manufacturers document image input, tool calling, streaming and prompt
caching. Native output is text; image-generation tools are outside this launch.
This does not assert these capabilities work through RC.47 or any supplier.

| Model | Input | Output | Cache read | Cache write |
| --- | ---: | ---: | ---: | --- |
| GPT-5.4 Mini | 0.75 | 4.50 | 0.075 | null; no additional write fee, ordinary input still billed |
| Claude Sonnet 5.5 | 2.00 | 10.00 | 0.10 | 2.50 (5m) or 4.00 (1h) |
| Gemini 3.5 Flash-Lite | 0.30 | 2.50 | 0.03 | null; standalone token write rate not listed |
| Gemini 3.8 Flash through 2026-12-31 | 0.75 | 3.75 | 0.075 | null; standalone token write rate not listed |
| Gemini 3.8 Flash from 2027-01-01 | 1.50 | 7.50 | 0.15 | null; standalone token write rate not listed |

Google cache storage: USD 1.00 per million token-hours for 3.5 Flash-Lite;
3.8 Flash USD 0.50 through 2026-12-31, then USD 1.00. These are not platform prices.

Official sources (also stored per model in JSON):

- [GPT-5.4 Mini specification and prices](https://developers.openai.com/api/docs/models/gpt-5.4-mini)
  and [OpenAI caching terms](https://developers.openai.com/api/docs/guides/prompt-caching).
- [Claude Sonnet 5.5 specification, cache prices and restrictions](https://platform.claude.com/docs/en/models/sonnet-5-5/overview),
  [exact output limit and streaming](https://platform.claude.com/docs/en/build-with-claude/effort),
  [tool-use support](https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview).
- [Gemini 3.5 Flash-Lite specification](https://ai.google.dev/gemini-api/docs/models/gemini-3.5-flash-lite),
  [Gemini 3.8 Flash specification](https://ai.google.dev/gemini-api/docs/models/gemini-3.8-flash),
  [paid standard and cache pricing](https://ai.google.dev/gemini-api/docs/pricing),
  [generateContent and streaming API](https://ai.google.dev/api/generate-content).

Provider restrictions matter: Sonnet 5.5 rejects temperature/top_p/top_k and
forced tool use; Gemini 3.8 rejects `minimal` thinking. These are recorded facts,
not adapter fixes. Older GPT-4.1/nano candidates marked deprecated and restricted
Gemini 2.5 models were not selected for a new launch.

## Loader and validation

For a future host call site, supply `common.Unmarshal` to `LoadEmbedded` and then
use `PublicView`. No second production JSON codec is selected by this package.
Decoder injection keeps tests independent of unrelated host/database startup;
test code uses the standard JSON codec only as its isolated fixture dependency.
There is no HTTP handler or initialization hook in this stage.

Validation checks identity/version uniqueness, explicit status and feature keys,
official HTTPS sources without credentials/query strings, dates/source references,
positive limits, launch scope, decimal prices/units, quote intervals and cache
TTLs, and separation of unconfigured prices and supplier verification evidence.

Focused tests:

```sh
go test -mod=readonly -count=1 ./service/productcatalog ./types/productcatalog
go test -mod=readonly -race -count=1 -cover ./service/productcatalog ./types/productcatalog
go vet -mod=readonly ./service/productcatalog ./types/productcatalog
```

Tests cover null/zero/false distinctions, private-field rejection and JSON
projection, dated rate transitions, cache TTL alternatives, invalid sources and
prices, all five verification states, evidence mismatch, and independent loads.
They use no provider API, database, Docker container or real credential. Unit
tests are not model-specific Mock calls or actual supplier verification.
