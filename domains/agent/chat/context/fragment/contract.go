package fragment

import (
	"errors"
	"fmt"
	"strings"
)

func (ref ContextRef) StableKey() string {
	namespace := strings.TrimSpace(ref.Namespace)
	id := strings.TrimSpace(ref.ID)
	schema := strings.TrimSpace(ref.Schema)
	durability := strings.TrimSpace(string(normalizeRefDurability(ref.Durability)))
	key := namespace + ":" + id
	if ref.Version != 0 {
		key += fmt.Sprintf("@v%d", ref.Version)
	}
	if ref.Range != nil {
		key += fmt.Sprintf("[%d:%d]", ref.Range.Start, ref.Range.End)
	}
	if schema != "" {
		key += "#" + schema
	}
	if durability != "" && durability != string(RefDurable) {
		key += "$" + durability
	}
	return key
}

func (ref ContextRef) EqualIdentity(other ContextRef) bool {
	if strings.TrimSpace(ref.Namespace) != strings.TrimSpace(other.Namespace) {
		return false
	}
	if strings.TrimSpace(ref.ID) != strings.TrimSpace(other.ID) {
		return false
	}
	if ref.Version != other.Version {
		return false
	}
	if strings.TrimSpace(ref.Schema) != strings.TrimSpace(other.Schema) {
		return false
	}
	if normalizeRefDurability(ref.Durability) != normalizeRefDurability(other.Durability) {
		return false
	}
	if ref.Range == nil || other.Range == nil {
		return ref.Range == nil && other.Range == nil
	}
	return ref.Range.Start == other.Range.Start && ref.Range.End == other.Range.End
}

func (ref ContextRef) IsDurable() bool {
	return normalizeRefDurability(ref.Durability) == RefDurable
}

func ValidateContextRef(ref ContextRef) error {
	if strings.TrimSpace(ref.Namespace) == "" {
		return errors.New("context ref namespace is required")
	}
	if strings.TrimSpace(ref.ID) == "" {
		return errors.New("context ref id is required")
	}
	if strings.TrimSpace(ref.Schema) == "" {
		return errors.New("context ref schema is required")
	}
	if ref.Durability != "" && !knownRefDurability(ref.Durability) {
		return fmt.Errorf("unknown context ref durability %q", ref.Durability)
	}
	if err := ValidateSchemaVersions([]SchemaVersion{{Name: ref.Schema, Version: CurrentSchemaVersion}}); err != nil {
		return err
	}
	if ref.Range != nil && ref.Range.End < ref.Range.Start {
		return fmt.Errorf("context ref range end %d before start %d", ref.Range.End, ref.Range.Start)
	}
	if strings.TrimSpace(ref.HashAlgo) != "" && ref.HashAlgo != HashAlgoSHA256 {
		return fmt.Errorf("unsupported context ref hash algo %q", ref.HashAlgo)
	}
	if hashScope := strings.TrimSpace(ref.HashScope); hashScope != "" && !knownHashScope(hashScope) {
		return fmt.Errorf("unsupported context ref hash scope %q", ref.HashScope)
	}
	if strings.TrimSpace(ref.ContentHash) != "" {
		if strings.TrimSpace(ref.HashAlgo) == "" {
			return errors.New("context ref hash algo is required when content hash is set")
		}
		if strings.TrimSpace(ref.HashScope) == "" {
			return errors.New("context ref hash scope is required when content hash is set")
		}
	}
	return nil
}

func ValidateSchemaVersions(versions []SchemaVersion) error {
	for _, version := range versions {
		name := strings.TrimSpace(version.Name)
		if name == "" {
			return errors.New("schema version name is required")
		}
		if !knownSchemaName(name) {
			return fmt.Errorf("unknown schema %q", name)
		}
		if version.Version != CurrentSchemaVersion {
			return fmt.Errorf("unsupported schema %q version %d", name, version.Version)
		}
	}
	return nil
}

func NewSummaryCoverage(summaryRef ContextRef, coveredRefs []ContextRef) SummaryCoverage {
	return SummaryCoverage{
		CoverageID:  "coverage:" + summaryRef.StableKey(),
		SummaryRef:  summaryRef,
		CoveredRefs: coveredRefs,
		Schema:      SchemaVersion{Name: SchemaSummaryCoverage, Version: CurrentSchemaVersion},
	}
}

func (edit ContextEdit) Targets(ref ContextRef) bool {
	for _, got := range edit.Refs {
		if got.EqualIdentity(ref) {
			return true
		}
	}
	return false
}

func CheckEditPreconditions(edit ContextEdit, stateRefs []ContextRef) []ContextConflict {
	var conflicts []ContextConflict
	if edit.Schema.Name != SchemaContextEdit || edit.Schema.Version != CurrentSchemaVersion {
		conflicts = append(conflicts, ContextConflict{
			Kind:     ConflictInvalidSchema,
			Expected: fmt.Sprintf("%s@%d", SchemaContextEdit, CurrentSchemaVersion),
			Actual:   fmt.Sprintf("%s@%d", edit.Schema.Name, edit.Schema.Version),
		})
	} else if err := ValidateSchemaVersions([]SchemaVersion{edit.Schema}); err != nil {
		conflicts = append(conflicts, ContextConflict{
			Kind:     ConflictInvalidSchema,
			Expected: fmt.Sprintf("%s@%d", SchemaContextEdit, CurrentSchemaVersion),
			Actual:   fmt.Sprintf("%s@%d", edit.Schema.Name, edit.Schema.Version),
		})
	}

	byKey := make(map[string]ContextRef, len(stateRefs))
	for _, ref := range stateRefs {
		byKey[ref.StableKey()] = ref
	}

	for key, expected := range edit.Preconditions.ExpectedHashes {
		ref, ok := byKey[key]
		if !ok {
			conflicts = append(conflicts, ContextConflict{Kind: ConflictMissingRef, Key: key, Expected: expected})
			continue
		}
		if ref.ContentHash != expected {
			conflicts = append(conflicts, ContextConflict{
				Kind:     ConflictContentHashMismatch,
				Key:      key,
				Expected: expected,
				Actual:   ref.ContentHash,
			})
		}
	}
	return conflicts
}

func WithContextRef(frag ContextFrag, ref ContextRef) ContextFrag {
	hash, hashErr := CanonicalFragmentHash(frag)
	hasExplicitID := strings.TrimSpace(ref.ID) != ""
	hasSourceID := strings.TrimSpace(frag.Provenance.SourceID) != ""
	ref.Namespace = firstNonEmpty(ref.Namespace, frag.Provenance.Source, "context_frag")
	switch {
	case hasExplicitID:
		ref.ID = strings.TrimSpace(ref.ID)
	case hasSourceID:
		ref.ID = strings.TrimSpace(frag.Provenance.SourceID)
	case hashErr == nil && hash.Value != "":
		ref.ID = hash.Value
	default:
		ref.ID = strings.TrimSpace(frag.ID)
	}
	ref.Schema = firstNonEmpty(ref.Schema, SchemaContextRef)
	if ref.Durability == "" {
		switch {
		case hasExplicitID || hasSourceID:
			ref.Durability = RefDurable
		case hashErr == nil && hash.Value != "":
			ref.Durability = RefSynthetic
		default:
			ref.Durability = RefDebug
		}
	}
	if hashErr == nil && !hasPreservedSourcePayloadHash(ref) {
		ref.HashAlgo = hash.Algo
		ref.HashScope = hash.Scope
		ref.ContentHash = hash.Value
	}
	frag.Ref = ref
	return frag
}

// UsesLockstepSchemaVersions reports the PR0 schema strategy: all context
// schemas share CurrentSchemaVersion until per-schema migration ranges exist.
func UsesLockstepSchemaVersions() bool {
	return true
}

func knownHashScope(scope string) bool {
	switch scope {
	case HashScopeCanonicalFragment, HashScopeSourcePayload:
		return true
	default:
		return false
	}
}

func hasPreservedSourcePayloadHash(ref ContextRef) bool {
	return strings.TrimSpace(ref.ContentHash) != "" &&
		strings.TrimSpace(ref.HashAlgo) == HashAlgoSHA256 &&
		strings.TrimSpace(ref.HashScope) == HashScopeSourcePayload
}

func ContextRefWarnings(ref ContextRef) []ValidationWarning {
	var warnings []ValidationWarning
	if err := ValidateContextRef(ref); err != nil {
		warnings = append(warnings, ValidationWarning{
			Code:    "invalid_context_ref",
			Message: err.Error(),
			Ref:     ref,
		})
	}
	if !ref.IsDurable() {
		warnings = append(warnings, ValidationWarning{
			Code:    "non_durable_context_ref",
			Message: fmt.Sprintf("context ref durability %q is for phase-1 trace only; durable sources should provide ContextRef IDs", ref.Durability),
			Ref:     ref,
		})
	}
	return warnings
}

func DefaultSchemaVersions() []SchemaVersion {
	return []SchemaVersion{
		{Name: SchemaContextManifest, Version: CurrentSchemaVersion},
		{Name: SchemaContextFrag, Version: CurrentSchemaVersion},
		{Name: SchemaContextRef, Version: CurrentSchemaVersion},
		{Name: SchemaContextEdit, Version: CurrentSchemaVersion},
		{Name: SchemaSummaryCoverage, Version: CurrentSchemaVersion},
		{Name: SchemaRenderPolicy, Version: CurrentSchemaVersion},
	}
}

func DefaultSlotRenderPolicies() []SlotRenderPolicy {
	return []SlotRenderPolicy{
		{Slot: SlotSystem, Order: "fragment_order", DedupeBy: "ref", CoverageAware: true, Target: "system"},
		{Slot: SlotBeforeHistory, Order: "fragment_order", DedupeBy: "ref", CoverageAware: true, Target: "messages"},
		{Slot: SlotHistory, Order: "fragment_order", DedupeBy: "ref", CoverageAware: true, Target: "messages"},
		{Slot: SlotAfterHistoryBeforeCurrent, Order: "fragment_order", DedupeBy: "ref", CoverageAware: true, Target: "messages"},
		{Slot: SlotCurrentUser, Order: "last_text_wins_images_append", DedupeBy: "ref", CoverageAware: true, Target: "query_inline_images"},
		{Slot: SlotAfterCurrent, Order: "fragment_order", DedupeBy: "ref", CoverageAware: true, Target: "messages"},
	}
}

func knownRefDurability(durability RefDurability) bool {
	switch durability {
	case RefDurable, RefSynthetic, RefDebug:
		return true
	default:
		return false
	}
}

func normalizeRefDurability(durability RefDurability) RefDurability {
	if durability == "" {
		return RefDurable
	}
	return durability
}

func knownSchemaName(name string) bool {
	switch name {
	case SchemaContextManifest, SchemaContextFrag, SchemaContextRef, SchemaContextEdit, SchemaSummaryCoverage, SchemaRenderPolicy:
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
