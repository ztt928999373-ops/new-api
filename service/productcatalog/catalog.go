// Copyright (c) QuantumNous and contributors.

// Package productcatalog loads and validates a display-only launch catalog.
// It has no database, HTTP, credential, routing, or billing side effects.
package productcatalog

import (
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	catalog "github.com/QuantumNous/new-api/types/productcatalog"
)

//go:embed product_catalog.json
var embedded []byte

var decimal = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)
var currency = regexp.MustCompile(`^[A-Z]{3}$`)
var evidenceID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*$`)

var features = []string{"text_input", "text_output", "image_input", "image_output", "tool_calling", "streaming", "cache_read", "cache_write"}

// Decoder must be the host's common.Unmarshal when integrated. Injection keeps
// this not-yet-wired catalog independent of database/auth initialization; it does
// not select a second production JSON engine. Each load returns owned data.
type Decoder func([]byte, any) error

func LoadEmbedded(decode Decoder, asOf time.Time) (catalog.Catalog, error) {
	return Load(embedded, decode, asOf)
}

func Load(data []byte, decode Decoder, asOf time.Time) (catalog.Catalog, error) {
	if decode == nil {
		return catalog.Catalog{}, errors.New("catalog decoder is required")
	}
	var c catalog.Catalog
	if err := decode(data, &c); err != nil {
		return catalog.Catalog{}, fmt.Errorf("decode product catalog: %w", err)
	}
	var document any
	if err := decode(data, &document); err != nil {
		return catalog.Catalog{}, fmt.Errorf("decode catalog document: %w", err)
	}
	if err := validateDocumentFields(document, reflect.TypeFor[catalog.Catalog]()); err != nil {
		return catalog.Catalog{}, err
	}
	if err := Validate(c, asOf); err != nil {
		return catalog.Catalog{}, err
	}
	return c, nil
}

// PublicView explicitly projects safe DTOs, even when server-side bindings exist.
// The caller owns c; this does not cache or mutate it. No route is synthesized.
func PublicView(c catalog.Catalog) catalog.PublicCatalog {
	result := catalog.PublicCatalog{SchemaVersion: c.SchemaVersion, Version: c.Version, VerifiedOn: c.VerifiedOn, Models: make([]catalog.PublicModel, 0, len(c.Models))}
	for _, model := range c.Models {
		result.Models = append(result.Models, model.PublicModel)
	}
	return result
}

func Validate(c catalog.Catalog, asOf time.Time) error {
	if c.SchemaVersion != 1 || strings.TrimSpace(c.Version) == "" || len(c.Models) == 0 {
		return errors.New("invalid catalog version or empty model list")
	}
	if err := checkedDate(c.VerifiedOn, asOf); err != nil {
		return fmt.Errorf("catalog verified_on: %w", err)
	}
	seen := make(map[string]bool)
	for _, model := range c.Models {
		if seen[model.PublicModelID] {
			return errors.New("duplicate public model ID")
		}
		seen[model.PublicModelID] = true
		if err := validateModel(model, asOf); err != nil {
			return fmt.Errorf("catalog model %q: %w", model.PublicModelID, err)
		}
	}
	return nil
}

func validateModel(m catalog.Model, asOf time.Time) error {
	if !identifier.MatchString(m.PublicModelID) || strings.TrimSpace(m.Name) == "" || !identifier.MatchString(m.Version.ID) || !slices.Contains([]string{"snapshot", "release_id", "stable_alias"}, m.Version.Kind) {
		return errors.New("invalid model identity/version")
	}
	hosts := map[string][]string{"openai": {"developers.openai.com", "openai.com"}, "anthropic": {"platform.claude.com", "docs.anthropic.com", "anthropic.com"}, "google": {"ai.google.dev", "cloud.google.com", "docs.cloud.google.com"}}
	allowed, ok := hosts[m.Manufacturer]
	if !ok || len(m.Sources) == 0 {
		return errors.New("unknown manufacturer or missing official sources")
	}
	sources := make(map[string]bool)
	for _, source := range m.Sources {
		u, err := url.Parse(source.URL)
		if err != nil || u.Scheme != "https" || !slices.Contains(allowed, u.Host) || u.User != nil || u.RawQuery != "" || u.Path == "" || source.ID == "" || sources[source.ID] {
			return errors.New("invalid, duplicate, non-official or credential-bearing source")
		}
		if err := checkedDate(source.CheckedOn, asOf); err != nil {
			return err
		}
		if source.OfficialUpdatedOn != nil {
			if err := checkedDate(*source.OfficialUpdatedOn, asOf); err != nil {
				return err
			}
		}
		sources[source.ID] = true
	}
	if err := sourceRefs(m.ManufacturerFacts.SourceIDs, sources); err != nil {
		return err
	}
	for _, limit := range []*int64{m.ManufacturerFacts.Limits.ContextTokens, m.ManufacturerFacts.Limits.MaxOutputTokens} {
		if limit != nil && *limit <= 0 {
			return errors.New("known token limits must be positive")
		}
	}
	limits := m.ManufacturerFacts.Limits
	if limits.ContextTokens != nil && limits.MaxOutputTokens != nil && *limits.MaxOutputTokens > *limits.ContextTokens {
		return errors.New("output limit exceeds context limit")
	}
	if err := validateModalities(m.ManufacturerFacts.InputModalities, m.ManufacturerFacts.OutputModalities); err != nil {
		return err
	}
	if !slices.Contains(m.ManufacturerFacts.InputModalities, "text") || !slices.Contains(m.ManufacturerFacts.OutputModalities, "text") || !slices.Equal(m.LaunchInputModalities, []string{"text"}) || !slices.Equal(m.LaunchOutputModalities, []string{"text"}) {
		return errors.New("launch scope must remain text input/output")
	}
	if len(m.ManufacturerFacts.Protocols) == 0 {
		return errors.New("missing documented protocol")
	}
	for _, protocol := range m.ManufacturerFacts.Protocols {
		if !slices.Contains([]string{"openai_chat", "openai_responses", "anthropic_messages", "gemini_generate_content"}, protocol) {
			return errors.New("unknown protocol")
		}
	}
	if err := validateReferencePrices(m.OfficialReferencePrices, sources, asOf); err != nil {
		return err
	}
	if err := validateSellingPrices(m.PlatformSellingPrices, asOf); err != nil {
		return err
	}
	if !slices.Contains([]string{"unassigned", "official_direct", "approved_aggregator"}, m.Supply.Mode) || (m.Supply.Mode == "unassigned" && m.Supply.SupplierName != nil) {
		return errors.New("supplier disclosure must not infer a supplier from manufacturer")
	}
	if err := validateVerification(m.PlatformSupport.Overall, asOf); err != nil {
		return err
	}
	if len(m.PlatformSupport.Features) != len(features) {
		return errors.New("all platform capability states must be explicit")
	}
	for _, feature := range features {
		v, exists := m.PlatformSupport.Features[feature]
		if !exists {
			return errors.New("missing platform capability state")
		}
		if err := validateVerification(v, asOf); err != nil {
			return err
		}
		if v.State == catalog.SupplierVerified && (m.Supply.Mode == "unassigned" || len(m.InternalRoutes) == 0) {
			return errors.New("supplier verification requires a real server-side assignment")
		}
		if m.PlatformSupport.Overall.State == catalog.SupplierVerified && v.State != catalog.SupplierVerified {
			return errors.New("overall supplier verification cannot overstate feature evidence")
		}
	}
	if m.PlatformSupport.Overall.State == catalog.SupplierVerified && (m.Supply.Mode == "unassigned" || len(m.InternalRoutes) == 0) {
		return errors.New("verified overall status requires server-side routes")
	}
	for _, route := range m.InternalRoutes {
		if route.ActualSupplierID == "" || route.ChannelID <= 0 || route.InternalModelID == "" || route.ConfigFingerprint == "" {
			return errors.New("incomplete private route assignment")
		}
	}
	return nil
}

// Reject misspelled fields and private configuration in the public artifact.
// Decode still goes through the injected host codec, not another JSON engine.
func validateDocumentFields(value any, target reflect.Type) error {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if value == nil {
		return nil
	}
	switch target.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return errors.New("catalog object expected")
		}
		fields := make(map[string]reflect.Type)
		pending := []reflect.Type{target}
		for len(pending) > 0 {
			next := pending[0]
			pending = pending[1:]
			for field := range next.NumField() {
				info := next.Field(field)
				tag := strings.Split(info.Tag.Get("json"), ",")[0]
				if tag == "-" || !info.IsExported() {
					continue
				}
				if info.Anonymous && tag == "" {
					pending = append(pending, info.Type)
					continue
				}
				if tag == "" {
					tag = info.Name
				}
				fields[tag] = info.Type
			}
		}
		for key, child := range object {
			typ, ok := fields[key]
			if !ok {
				return errors.New("unknown or private field in public catalog document")
			}
			if err := validateDocumentFields(child, typ); err != nil {
				return err
			}
		}
	case reflect.Slice:
		array, ok := value.([]any)
		if !ok {
			return errors.New("catalog array expected")
		}
		for _, child := range array {
			if err := validateDocumentFields(child, target.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return errors.New("catalog map expected")
		}
		for _, child := range object {
			if err := validateDocumentFields(child, target.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkedDate(value string, asOf time.Time) error {
	date, err := time.Parse(time.DateOnly, value)
	if err != nil || asOf.IsZero() || date.Format(time.DateOnly) > asOf.Format(time.DateOnly) {
		return errors.New("invalid or future verification date")
	}
	return nil
}

func sourceRefs(refs []string, sources map[string]bool) error {
	if len(refs) == 0 {
		return errors.New("missing source references")
	}
	for _, ref := range refs {
		if !sources[ref] {
			return errors.New("unknown source reference")
		}
	}
	return nil
}

func validateModalities(input, output []string) error {
	if len(input) == 0 || len(output) == 0 {
		return errors.New("missing modalities")
	}
	for _, values := range [][]string{input, output} {
		seen := make(map[string]bool)
		for _, value := range values {
			if !slices.Contains([]string{"text", "image", "audio", "video", "pdf"}, value) || seen[value] {
				return errors.New("unknown or duplicate modality")
			}
			seen[value] = true
		}
	}
	return nil
}

func validateReferencePrices(p catalog.ReferencePricing, sources map[string]bool, asOf time.Time) error {
	if p.PriceType != "official_reference" || p.Currency != "USD" || p.Unit != "per_1m_tokens" || len(p.Quotes) == 0 {
		return errors.New("official price currency/unit/quotes missing")
	}
	if err := checkedDate(p.CheckedOn, asOf); err != nil {
		return err
	}
	if p.OfficialUpdatedOn != nil {
		if err := checkedDate(*p.OfficialUpdatedOn, asOf); err != nil {
			return err
		}
	}
	if err := sourceRefs(p.SourceIDs, sources); err != nil {
		return err
	}
	ids := make(map[string]bool)
	for _, q := range p.Quotes {
		if q.ID == "" || ids[q.ID] || q.Conditions == "" {
			return errors.New("quote identity/conditions missing or duplicate")
		}
		ids[q.ID] = true
		if err := validateTokenPrices(q.Prices); err != nil {
			return err
		}
		for _, date := range []*string{q.EffectiveFrom, q.EffectiveUntil} {
			if date != nil {
				if _, err := time.Parse(time.DateOnly, *date); err != nil {
					return errors.New("invalid effective price date")
				}
			}
		}
		if q.EffectiveFrom != nil && q.EffectiveUntil != nil && *q.EffectiveFrom >= *q.EffectiveUntil {
			return errors.New("empty/inverted price interval")
		}
		if q.CacheWriteTTLSeconds != nil && *q.CacheWriteTTLSeconds <= 0 {
			return errors.New("invalid cache write TTL")
		}
	}
	for i, quote := range p.Quotes {
		for _, other := range p.Quotes[i+1:] {
			sameTTL := (quote.CacheWriteTTLSeconds == nil && other.CacheWriteTTLSeconds == nil) || (quote.CacheWriteTTLSeconds != nil && other.CacheWriteTTLSeconds != nil && *quote.CacheWriteTTLSeconds == *other.CacheWriteTTLSeconds)
			if sameTTL && intervalsOverlap(quote.EffectiveFrom, quote.EffectiveUntil, other.EffectiveFrom, other.EffectiveUntil) {
				return errors.New("overlapping reference quote intervals for the same cache TTL")
			}
		}
	}
	for _, charge := range p.AdditionalCharges {
		for _, date := range []*string{charge.EffectiveFrom, charge.EffectiveUntil} {
			if date != nil {
				if _, err := time.Parse(time.DateOnly, *date); err != nil {
					return errors.New("invalid additional charge date")
				}
			}
		}
		if charge.EffectiveFrom != nil && charge.EffectiveUntil != nil && *charge.EffectiveFrom >= *charge.EffectiveUntil {
			return errors.New("empty additional charge interval")
		}
		if charge.Kind != "cache_storage" || len(charge.Amount) > 32 || !decimal.MatchString(charge.Amount) || charge.Unit != "per_1m_token_hours" || charge.Notes == "" {
			return errors.New("invalid additional reference charge")
		}
	}
	for i, charge := range p.AdditionalCharges {
		for _, other := range p.AdditionalCharges[i+1:] {
			if charge.Kind == other.Kind && intervalsOverlap(charge.EffectiveFrom, charge.EffectiveUntil, other.EffectiveFrom, other.EffectiveUntil) {
				return errors.New("overlapping additional reference charge intervals")
			}
		}
	}
	return nil
}

// ReferenceQuotesOn selects dated documentation only; it never computes a bill.
// A null start is not a claim about prices before the observation date.
func ReferenceQuotesOn(p catalog.ReferencePricing, date string) ([]catalog.ReferenceQuote, error) {
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		return nil, err
	}
	result := make([]catalog.ReferenceQuote, 0)
	for _, quote := range p.Quotes {
		start := p.CheckedOn
		if quote.EffectiveFrom != nil {
			start = *quote.EffectiveFrom
		}
		if date >= start && (quote.EffectiveUntil == nil || date < *quote.EffectiveUntil) {
			result = append(result, quote)
		}
	}
	return result, nil
}

func intervalsOverlap(aStart, aEnd, bStart, bEnd *string) bool {
	return (aEnd == nil || bStart == nil || *bStart < *aEnd) && (bEnd == nil || aStart == nil || *aStart < *bEnd)
}

func validateTokenPrices(prices catalog.TokenPrices) error {
	for _, value := range []*string{prices.Input, prices.Output, prices.CacheRead, prices.CacheWrite} {
		if value != nil && (len(*value) > 32 || !decimal.MatchString(*value)) {
			return errors.New("price must be null or a nonnegative decimal string")
		}
	}
	return nil
}

func validateSellingPrices(p catalog.SellingPricing, asOf time.Time) error {
	if p.PriceType != "platform_selling" {
		return errors.New("platform selling price type is required")
	}
	if err := validateTokenPrices(p.Prices); err != nil {
		return err
	}
	if p.State == "unconfigured" {
		if p.Currency != nil || p.Unit != nil || p.Source != nil || p.PricingVersion != nil || p.UpdatedOn != nil || p.EffectiveFrom != nil || p.EffectiveUntil != nil || p.Prices.Input != nil || p.Prices.Output != nil || p.Prices.CacheRead != nil || p.Prices.CacheWrite != nil {
			return errors.New("unconfigured selling prices must stay null")
		}
		return nil
	}
	if p.State != "configured" || p.Currency == nil || !currency.MatchString(*p.Currency) || p.Unit == nil || *p.Unit != "per_1m_tokens" || p.Source == nil || *p.Source != "new_api_effective_pricing" || p.PricingVersion == nil || *p.PricingVersion == "" || p.UpdatedOn == nil {
		return errors.New("platform prices require separate effective-pricing provenance")
	}
	for _, date := range []*string{p.EffectiveFrom, p.EffectiveUntil} {
		if date != nil {
			if _, err := time.Parse(time.DateOnly, *date); err != nil {
				return errors.New("invalid selling price effective date")
			}
		}
	}
	if p.EffectiveFrom != nil && p.EffectiveUntil != nil && *p.EffectiveFrom >= *p.EffectiveUntil {
		return errors.New("empty/inverted selling price interval")
	}
	return checkedDate(*p.UpdatedOn, asOf)
}

func validateVerification(v catalog.Verification, asOf time.Time) error {
	if !slices.Contains([]catalog.ValidationState{catalog.Unverified, catalog.MockPassed, catalog.SupplierVerified, catalog.Partial, catalog.Unsupported}, v.State) || strings.TrimSpace(v.Notes) == "" {
		return errors.New("unknown/missing platform verification state or explanation")
	}
	if v.State == catalog.Unverified {
		if v.Kind != nil || v.EvidenceID != nil || v.TestedOn != nil {
			return errors.New("unverified status cannot carry a passing test claim")
		}
		return nil
	}
	if v.State == catalog.Unsupported && v.Kind == nil && v.EvidenceID == nil && v.TestedOn == nil {
		return nil
	}
	if v.Kind == nil || !slices.Contains([]string{"mock", "supplier"}, *v.Kind) || v.EvidenceID == nil || !evidenceID.MatchString(*v.EvidenceID) || v.TestedOn == nil {
		return errors.New("verification evidence is required")
	}
	if (v.State == catalog.SupplierVerified && *v.Kind != "supplier") || (v.State == catalog.MockPassed && *v.Kind != "mock") {
		return errors.New("mock and supplier evidence must not be conflated")
	}
	return checkedDate(*v.TestedOn, asOf)
}
