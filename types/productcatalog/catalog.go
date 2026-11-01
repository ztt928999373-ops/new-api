// Copyright (c) QuantumNous and contributors.

// Package productcatalog defines display-only model facts. None of these types
// are GORM models, routing rules, or executable billing configuration.
package productcatalog

// ValidationState describes platform evidence, never manufacturer marketing.
type ValidationState string

const (
	Unverified       ValidationState = "unverified"
	MockPassed       ValidationState = "mock_passed"
	SupplierVerified ValidationState = "supplier_verified"
	Partial          ValidationState = "partial"
	Unsupported      ValidationState = "unsupported"
)

type Source struct {
	ID                string  `json:"id"`
	URL               string  `json:"url"`
	CheckedOn         string  `json:"checked_on"`
	OfficialUpdatedOn *string `json:"official_updated_on"`
}

type Version struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // snapshot, release_id, or stable_alias (not immutable)
}

type Limits struct {
	ContextTokens   *int64 `json:"context_tokens"`
	MaxOutputTokens *int64 `json:"max_output_tokens"`
}

// Null is unknown; false is an explicit negative claim. Do not omit either.
type Capabilities struct {
	ImageInput  *bool `json:"image_input"`
	ImageOutput *bool `json:"image_output"`
	ToolCalling *bool `json:"tool_calling"`
	Streaming   *bool `json:"streaming"`
	CacheRead   *bool `json:"cache_read"`
	CacheWrite  *bool `json:"cache_write"`
}

type ManufacturerFacts struct {
	Protocols        []string     `json:"protocols"`
	InputModalities  []string     `json:"input_modalities"`
	OutputModalities []string     `json:"output_modalities"`
	Limits           Limits       `json:"limits"`
	Capabilities     Capabilities `json:"capabilities"`
	SourceIDs        []string     `json:"source_ids"`
	Notes            []string     `json:"notes"`
}

// Amounts are nullable decimal strings, never binary floats. "0" is explicit;
// nil is unknown/not separately listed, and must never be rendered as free.
type TokenPrices struct {
	Input      *string `json:"input"`
	Output     *string `json:"output"`
	CacheRead  *string `json:"cache_read"`
	CacheWrite *string `json:"cache_write"`
}

type ReferenceQuote struct {
	ID                   string      `json:"id"`
	EffectiveFrom        *string     `json:"effective_from"`
	EffectiveUntil       *string     `json:"effective_until"` // exclusive
	CacheWriteTTLSeconds *int64      `json:"cache_write_ttl_seconds"`
	Prices               TokenPrices `json:"prices"`
	Conditions           string      `json:"conditions"`
}

type AdditionalCharge struct {
	EffectiveFrom  *string `json:"effective_from"`
	EffectiveUntil *string `json:"effective_until"`
	Kind           string  `json:"kind"`
	Amount         string  `json:"amount"`
	Unit           string  `json:"unit"`
	Notes          string  `json:"notes"`
}

type ReferencePricing struct {
	PriceType         string             `json:"price_type"` // official_reference
	Currency          string             `json:"currency"`
	Unit              string             `json:"unit"`
	CheckedOn         string             `json:"checked_on"`
	OfficialUpdatedOn *string            `json:"official_updated_on"`
	SourceIDs         []string           `json:"source_ids"`
	Quotes            []ReferenceQuote   `json:"quotes"`
	AdditionalCharges []AdditionalCharge `json:"additional_charges"`
}

// SellingPricing is intentionally not initialized from ReferencePricing.
// A future integration must supply effective New API prices and their version.
type SellingPricing struct {
	PriceType      string      `json:"price_type"` // platform_selling
	State          string      `json:"state"`
	Currency       *string     `json:"currency"`
	Unit           *string     `json:"unit"`
	Prices         TokenPrices `json:"prices"`
	Source         *string     `json:"source"`
	PricingVersion *string     `json:"pricing_version"`
	UpdatedOn      *string     `json:"updated_on"`
	EffectiveFrom  *string     `json:"effective_from"`
	EffectiveUntil *string     `json:"effective_until"` // exclusive
}

type Verification struct {
	State      ValidationState `json:"state"`
	Kind       *string         `json:"kind"` // mock or supplier; not inferred
	EvidenceID *string         `json:"evidence_id"`
	TestedOn   *string         `json:"tested_on"`
	Notes      string          `json:"notes"`
}

type PlatformSupport struct {
	Overall  Verification            `json:"overall"`
	Features map[string]Verification `json:"features"`
}

type SupplyDisclosure struct {
	Mode         string  `json:"mode"`          // unassigned, official_direct, approved_aggregator
	SupplierName *string `json:"supplier_name"` // only an explicitly approved public label
}

// PublicModel is an allowlist. Never embed Channel, credentials or raw settings.
type PublicModel struct {
	PublicModelID           string            `json:"public_model_id"`
	Name                    string            `json:"name"`
	Manufacturer            string            `json:"manufacturer"`
	Version                 Version           `json:"manufacturer_version"`
	Sources                 []Source          `json:"sources"`
	ManufacturerFacts       ManufacturerFacts `json:"manufacturer_facts"`
	OfficialReferencePrices ReferencePricing  `json:"official_reference_prices"`
	PlatformSellingPrices   SellingPricing    `json:"platform_selling_prices"`
	PlatformSupport         PlatformSupport   `json:"platform_support"`
	Supply                  SupplyDisclosure  `json:"supply"`
	LaunchInputModalities   []string          `json:"launch_input_modalities"`
	LaunchOutputModalities  []string          `json:"launch_output_modalities"`
}

// InternalRoute is server-only. Actual supplier, channel and upstream model IDs
// are distinct from manufacturer/public IDs; no field may enter public JSON.
// It stores identifiers only, not keys, passwords or supplier-private blobs.
type InternalRoute struct {
	ActualSupplierID  string `json:"-"`
	ChannelID         int    `json:"-"`
	InternalModelID   string `json:"-"`
	ConfigFingerprint string `json:"-"`
}

type Model struct {
	PublicModel
	InternalRoutes []InternalRoute `json:"-"`
}

type Catalog struct {
	SchemaVersion int     `json:"schema_version"`
	Version       string  `json:"version"`
	VerifiedOn    string  `json:"verified_on"`
	Models        []Model `json:"models"`
}

type PublicCatalog struct {
	SchemaVersion int           `json:"schema_version"`
	Version       string        `json:"version"`
	VerifiedOn    string        `json:"verified_on"`
	Models        []PublicModel `json:"models"`
}
