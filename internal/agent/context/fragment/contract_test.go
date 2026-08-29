package contextfrag

import (
	"encoding/json"
	"reflect"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func TestContextRefRoundTripPreservesReducerIdentity(t *testing.T) {
	t.Parallel()

	ref := ContextRef{
		Namespace:   "history",
		ID:          "row-1",
		Version:     2,
		Range:       &ContentRange{Start: 10, End: 20},
		HashAlgo:    HashAlgoSHA256,
		ContentHash: "abc123",
		HashScope:   HashScopeCanonicalFragment,
		Schema:      SchemaContextRef,
		Durability:  RefDurable,
	}

	raw, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("marshal context ref: %v", err)
	}
	var got ContextRef
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal context ref: %v", err)
	}

	if !ref.EqualIdentity(got) {
		t.Fatalf("roundtrip ref identity mismatch: got %#v want %#v", got, ref)
	}
	if got.StableKey() != ref.StableKey() {
		t.Fatalf("stable key = %q, want %q", got.StableKey(), ref.StableKey())
	}
	for _, op := range []ContextEditOp{EditReplace, EditRemove, EditCover} {
		edit := ContextEdit{Op: op, Slot: SlotHistory, Refs: []ContextRef{got}}
		if !edit.Targets(ref) {
			t.Fatalf("%s edit should target round-tripped ref", op)
		}
	}
	if err := ValidateContextRef(ContextRef{Namespace: "history", Schema: SchemaContextRef}); err == nil {
		t.Fatal("context ref without ID should fail validation")
	}
}

func TestCanonicalFragmentHashIsStableAndIgnoresDebugID(t *testing.T) {
	t.Parallel()

	frag := TextFrag(TextFragInput{
		ID:         "debug-a",
		Kind:       KindConversationEvent,
		Role:       sdk.MessageRoleUser,
		Slot:       SlotHistory,
		Text:       "hello",
		Priority:   70,
		CacheClass: CacheNever,
		Trust:      TrustExternal,
		Source:     "history",
		SourceID:   "row-1",
		Collector:  "test",
	})
	same := frag
	same.ID = "debug-b"
	same.Provenance.Index = 42
	changed := frag
	changed.Parts = append([]Part(nil), frag.Parts...)
	changed.Parts[0].Text = "hello again"

	hash, err := CanonicalFragmentHash(frag)
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	sameHash, err := CanonicalFragmentHash(same)
	if err != nil {
		t.Fatalf("canonical hash for same content: %v", err)
	}
	changedHash, err := CanonicalFragmentHash(changed)
	if err != nil {
		t.Fatalf("canonical hash for changed content: %v", err)
	}

	if hash.Algo != HashAlgoSHA256 || hash.Scope != HashScopeCanonicalFragment {
		t.Fatalf("hash metadata = %#v", hash)
	}
	if hash.Value == "" {
		t.Fatal("canonical hash should not be empty")
	}
	if sameHash.Value != hash.Value {
		t.Fatalf("canonical hash should ignore debug ID and positional index: got %q want %q", sameHash.Value, hash.Value)
	}
	if changedHash.Value == hash.Value {
		t.Fatalf("canonical hash should change when fragment content changes: %q", hash.Value)
	}
}

func TestCanonicalFragmentHashIncludesSectionPolicy(t *testing.T) {
	t.Parallel()

	base := TextFrag(TextFragInput{
		ID:                 "system.skills",
		Kind:               KindSkillsCatalog,
		Role:               sdk.MessageRoleSystem,
		Slot:               SlotSystem,
		Text:               "skills",
		RetentionTier:      RetentionRequired,
		DropPriority:       10,
		RequiredCapability: "use_skill",
		Render: RenderPolicy{
			Format:      RenderMarkdown,
			GroupID:     "system.skills",
			GroupJoiner: "\n",
		},
	})

	baseHash, err := CanonicalFragmentHash(base)
	if err != nil {
		t.Fatalf("canonical base hash: %v", err)
	}
	mutations := []struct {
		name   string
		mutate func(*ContextFrag)
	}{
		{name: "retention tier", mutate: func(frag *ContextFrag) { frag.RetentionTier = RetentionPreferred }},
		{name: "drop priority", mutate: func(frag *ContextFrag) { frag.DropPriority = 20 }},
		{name: "required capability", mutate: func(frag *ContextFrag) { frag.RequiredCapability = "other_tool" }},
		{name: "render group", mutate: func(frag *ContextFrag) { frag.Render.GroupID = "other.group" }},
		{name: "render joiner", mutate: func(frag *ContextFrag) { frag.Render.GroupJoiner = "\n\n" }},
	}
	for _, mutation := range mutations {
		mutation := mutation
		t.Run(mutation.name, func(t *testing.T) {
			t.Parallel()
			changed := base
			mutation.mutate(&changed)
			changedHash, err := CanonicalFragmentHash(changed)
			if err != nil {
				t.Fatalf("canonical changed hash: %v", err)
			}
			if changedHash.Value == baseHash.Value {
				t.Fatalf("canonical hash must change with %s", mutation.name)
			}
		})
	}
}

func TestDropPriorityHigherValuesDropFirst(t *testing.T) {
	t.Parallel()

	if !DropPriority(20).DropsBefore(10) {
		t.Fatal("higher drop priority must drop before a lower value")
	}
	if DropPriority(10).DropsBefore(20) {
		t.Fatal("lower drop priority must survive longer than a higher value")
	}
}

func TestMessageFragAndRepairPreserveSectionPolicy(t *testing.T) {
	t.Parallel()

	policy := RenderPolicy{Format: RenderSDKMessage, GroupID: "messages", GroupJoiner: "\n"}
	frag := MessageFrag(MessageFragInput{
		ID:                 "message.001",
		Message:            sdk.UserMessage("before"),
		Kind:               KindConversationEvent,
		Slot:               SlotHistory,
		RetentionTier:      RetentionPreferred,
		DropPriority:       25,
		RequiredCapability: "read",
		Render:             policy,
	})
	rebuilt := RebuildFragMessage(frag, sdk.AssistantMessage("after"))

	if rebuilt.RetentionTier != frag.RetentionTier || rebuilt.DropPriority != frag.DropPriority ||
		rebuilt.RequiredCapability != frag.RequiredCapability || rebuilt.Render != policy {
		t.Fatalf("rebuilt policy = %#v, want %#v", rebuilt, frag)
	}
}

func TestCanonicalFragmentHashGoldenValue(t *testing.T) {
	t.Parallel()

	frag := TextFrag(TextFragInput{
		ID:         "golden",
		Kind:       KindConversationEvent,
		Role:       sdk.MessageRoleUser,
		Slot:       SlotHistory,
		Text:       "hello",
		Priority:   70,
		CacheClass: CacheNever,
		Trust:      TrustExternal,
		Source:     "test",
		SourceID:   "row-1",
		Collector:  "golden_test",
	})
	hash, err := CanonicalFragmentHash(frag)
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	const want = "a33139d731966124ebcadf9eb4f2ce815b63650e866794086fc587c504193764"
	if hash.Value != want {
		t.Fatalf("golden hash drifted: got %q, want %q — if the canonical struct shape changed intentionally, update this value", hash.Value, want)
	}
}

func TestBuildManifestItemCarriesSectionPolicy(t *testing.T) {
	t.Parallel()

	render := RenderPolicy{Format: RenderMarkdown, GroupID: "system.skills", GroupJoiner: "\n"}
	frag := TextFrag(TextFragInput{
		ID:                 "system.skill.alpha",
		Kind:               KindSkillsCatalog,
		Slot:               SlotSystem,
		Text:               "alpha",
		RetentionTier:      RetentionOptional,
		DropPriority:       30,
		RequiredCapability: "use_skill",
		Render:             render,
	})
	manifest := BuildManifest([]ContextFrag{frag})

	if len(manifest.Items) != 1 {
		t.Fatalf("manifest items = %d, want 1", len(manifest.Items))
	}
	item := manifest.Items[0]
	if item.RetentionTier != RetentionOptional || item.DropPriority != 30 ||
		item.RequiredCapability != "use_skill" || item.Render != render {
		t.Fatalf("manifest section policy = %#v", item)
	}
}

func TestCanonicalFragmentHashDiscriminatesSDKMessagePartTypes(t *testing.T) {
	t.Parallel()

	textFrag := MessageFrag(MessageFragInput{
		ID:      "message.text",
		Message: sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: "same"}}},
		Kind:    KindConversationEvent,
		Slot:    SlotHistory,
	})
	reasoningFrag := MessageFrag(MessageFragInput{
		ID:      "message.reasoning",
		Message: sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ReasoningPart{Text: "same"}}},
		Kind:    KindConversationEvent,
		Slot:    SlotHistory,
	})

	textHash, err := CanonicalFragmentHash(textFrag)
	if err != nil {
		t.Fatalf("text hash: %v", err)
	}
	reasoningHash, err := CanonicalFragmentHash(reasoningFrag)
	if err != nil {
		t.Fatalf("reasoning hash: %v", err)
	}
	if textHash.Value == reasoningHash.Value {
		t.Fatalf("text and reasoning SDK message parts must not collide: %q", textHash.Value)
	}
}

func TestCanonicalFragmentHashIncludesNativeImageSDKPayload(t *testing.T) {
	t.Parallel()

	base := ImageFrag("image", []sdk.ImagePart{{
		Image:     "data:image/png;base64,abc",
		MediaType: "image/png",
	}}, Scope{}, "test")
	withCache := ImageFrag("image", []sdk.ImagePart{{
		Image:        "data:image/png;base64,abc",
		MediaType:    "image/png",
		CacheControl: &sdk.CacheControl{Type: "ephemeral"},
	}}, Scope{}, "test")

	baseHash, err := CanonicalFragmentHash(base)
	if err != nil {
		t.Fatalf("base image hash: %v", err)
	}
	cacheHash, err := CanonicalFragmentHash(withCache)
	if err != nil {
		t.Fatalf("cache-control image hash: %v", err)
	}
	if baseHash.Value == cacheHash.Value {
		t.Fatalf("image fragments with different SDK cache control must not collide: %q", baseHash.Value)
	}
}

func TestWithContextRefPreservesExplicitSourcePayloadHash(t *testing.T) {
	t.Parallel()

	frag := TextFrag(TextFragInput{
		ID:         "history.001",
		Kind:       KindConversationEvent,
		Role:       sdk.MessageRoleUser,
		Slot:       SlotHistory,
		Text:       "hello",
		Priority:   70,
		CacheClass: CacheNever,
		Trust:      TrustExternal,
		Scope:      Scope{ChatID: "request-scope"},
		Source:     "history",
		SourceID:   "row-1",
		Collector:  "test",
	})
	frag = WithContextRef(frag, ContextRef{
		Namespace:   "history",
		ID:          "row-1",
		Version:     1,
		HashAlgo:    HashAlgoSHA256,
		HashScope:   HashScopeSourcePayload,
		ContentHash: "source-hash",
		Schema:      SchemaContextRef,
		Durability:  RefDurable,
	})

	if frag.Ref.HashScope != HashScopeSourcePayload || frag.Ref.ContentHash != "source-hash" {
		t.Fatalf("explicit source payload hash should be preserved: %#v", frag.Ref)
	}
	if err := ValidateContextRef(frag.Ref); err != nil {
		t.Fatalf("source payload ref should validate: %#v: %v", frag.Ref, err)
	}
}

func TestWithContextRefRecomputesCanonicalFragmentHash(t *testing.T) {
	t.Parallel()

	frag := TextFrag(TextFragInput{
		ID:         "history.001",
		Kind:       KindConversationEvent,
		Role:       sdk.MessageRoleUser,
		Slot:       SlotHistory,
		Text:       "hello",
		Priority:   70,
		CacheClass: CacheNever,
		Trust:      TrustExternal,
		Source:     "history",
		SourceID:   "row-1",
		Collector:  "test",
	})
	normalized := WithContextRef(frag, ContextRef{
		Namespace:  "history",
		ID:         "row-1",
		Version:    1,
		Schema:     SchemaContextRef,
		Durability: RefDurable,
	})
	mutated := frag
	mutated.Parts = append([]Part(nil), frag.Parts...)
	mutated.Parts[0].Text = "hello again"
	expected, err := CanonicalFragmentHash(mutated)
	if err != nil {
		t.Fatalf("canonical hash for mutated frag: %v", err)
	}

	got := WithContextRef(mutated, normalized.Ref)

	if got.Ref.HashScope != HashScopeCanonicalFragment || got.Ref.HashAlgo != HashAlgoSHA256 {
		t.Fatalf("canonical hash metadata should be refreshed: %#v", got.Ref)
	}
	if got.Ref.ContentHash != expected.Value {
		t.Fatalf("canonical hash should be recomputed from current fragment: got %q want %q", got.Ref.ContentHash, expected.Value)
	}
}

func TestWithContextRefRepairsIncompleteExplicitHash(t *testing.T) {
	t.Parallel()

	frag := TextFrag(TextFragInput{
		ID:         "history.001",
		Kind:       KindConversationEvent,
		Role:       sdk.MessageRoleUser,
		Slot:       SlotHistory,
		Text:       "hello",
		Priority:   70,
		CacheClass: CacheNever,
		Trust:      TrustExternal,
		Source:     "history",
		SourceID:   "row-1",
		Collector:  "test",
	})
	frag = WithContextRef(frag, ContextRef{
		Namespace:   "history",
		ID:          "row-1",
		Version:     1,
		ContentHash: "bare-hash",
		Schema:      SchemaContextRef,
		Durability:  RefDurable,
	})

	if frag.Ref.ContentHash == "bare-hash" || frag.Ref.HashScope != HashScopeCanonicalFragment || frag.Ref.HashAlgo != HashAlgoSHA256 {
		t.Fatalf("incomplete explicit hash should be repaired with canonical hash: %#v", frag.Ref)
	}
	if err := ValidateContextRef(frag.Ref); err != nil {
		t.Fatalf("repaired ref should validate: %#v: %v", frag.Ref, err)
	}
}

func TestSerdeRoundTripsContextFragManifestEditAndCoverage(t *testing.T) {
	t.Parallel()

	frag := TextFrag(TextFragInput{
		ID:         "history.001",
		Kind:       KindConversationEvent,
		Role:       sdk.MessageRoleUser,
		Slot:       SlotHistory,
		Text:       "hello",
		Priority:   70,
		CacheClass: CacheNever,
		Trust:      TrustExternal,
		Source:     "history",
		SourceID:   "row-1",
		Collector:  "test",
	})
	frag = WithContextRef(frag, ContextRef{
		Namespace: "history",
		ID:        "row-1",
		Version:   1,
		Schema:    SchemaContextRef,
	})

	roundTripJSON(t, frag, func(got ContextFrag) {
		if !frag.Ref.EqualIdentity(got.Ref) {
			t.Fatalf("frag ref mismatch after roundtrip: got %#v want %#v", got.Ref, frag.Ref)
		}
		if got.Ref.ContentHash == "" {
			t.Fatal("frag ref should carry canonical content hash")
		}
	})
	msgFrag := WithContextRef(MessageFrag(MessageFragInput{
		ID:         "message.001",
		Message:    sdk.UserMessage("from sdk", sdk.ImagePart{Image: "data:image/png;base64,abc", MediaType: "image/png"}),
		Kind:       KindConversationEvent,
		Slot:       SlotHistory,
		Priority:   70,
		CacheClass: CacheNever,
		Trust:      TrustExternal,
		Source:     "history",
		SourceID:   "row-2",
		Collector:  "test",
	}), ContextRef{
		Namespace: "history",
		ID:        "row-2",
		Version:   1,
		Schema:    SchemaContextRef,
	})
	roundTripJSON(t, msgFrag, func(got ContextFrag) {
		rendered := Render([]ContextFrag{got})
		if len(rendered.Messages) != 1 {
			t.Fatalf("round-tripped sdk message fragment rendered %d messages, want 1", len(rendered.Messages))
		}
		if rendered.Messages[0].Role != sdk.MessageRoleUser {
			t.Fatalf("round-tripped sdk message role = %q, want user", rendered.Messages[0].Role)
		}
		if !reflect.DeepEqual(rendered.Messages[0], *msgFrag.Parts[0].Message) {
			t.Fatalf("round-tripped sdk message mismatch: got %#v want %#v", rendered.Messages[0], msgFrag.Parts[0].Message)
		}
	})

	manifest := BuildManifest([]ContextFrag{frag, msgFrag})
	roundTripJSON(t, manifest, func(got Manifest) {
		if !hasSchemaVersion(got.SchemaVersions, SchemaContextManifest, CurrentSchemaVersion) {
			t.Fatalf("manifest schema versions missing manifest version: %#v", got.SchemaVersions)
		}
		if len(got.Items) != 2 || !frag.Ref.EqualIdentity(got.Items[0].Ref) {
			t.Fatalf("manifest item ref mismatch: %#v", got.Items)
		}
		if len(got.SlotPolicies) == 0 {
			t.Fatal("manifest should include per-slot render policies")
		}
		if got.Counts.Images != 1 {
			t.Fatalf("manifest image count = %d, want 1", got.Counts.Images)
		}
	})

	edit := ContextEdit{
		EditID: "edit-1",
		Slot:   SlotHistory,
		Op:     EditReplace,
		Refs:   []ContextRef{frag.Ref},
		Payload: []ContextFrag{
			frag,
		},
		Preconditions: EditPreconditions{
			ExpectedRevision: "rev-1",
			MaxSequence:      42,
			ExpectedHashes:   map[string]string{frag.Ref.StableKey(): frag.Ref.ContentHash},
		},
		Schema: SchemaVersion{Name: SchemaContextEdit, Version: CurrentSchemaVersion},
	}
	roundTripJSON(t, edit, func(got ContextEdit) {
		if !got.Targets(frag.Ref) {
			t.Fatalf("round-tripped edit should target payload ref: %#v", got)
		}
	})

	coverage := SummaryCoverage{
		CoverageID:   "coverage-1",
		SummaryRef:   ContextRef{Namespace: "summary", ID: "summary-1", Schema: SchemaContextRef, Durability: RefDurable},
		CoveredRefs:  []ContextRef{frag.Ref},
		TraceFragIDs: []string{frag.ID},
		Schema:       SchemaVersion{Name: SchemaSummaryCoverage, Version: CurrentSchemaVersion},
	}
	roundTripJSON(t, coverage, func(got SummaryCoverage) {
		if len(got.CoveredRefs) != 1 || !got.CoveredRefs[0].EqualIdentity(frag.Ref) {
			t.Fatalf("coverage refs mismatch after roundtrip: %#v", got.CoveredRefs)
		}
		if len(got.TraceFragIDs) != 1 || got.TraceFragIDs[0] != frag.ID {
			t.Fatalf("coverage trace frag IDs mismatch after roundtrip: %#v", got.TraceFragIDs)
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal coverage: %v", err)
		}
		if json.Valid(raw) && containsJSONField(raw, "covered_frag_ids") {
			t.Fatalf("coverage JSON should use trace_frag_ids, got %s", raw)
		}
	})

	group := ContinuityGroup{
		ID:               "tool-call-1",
		Kind:             "tool_call",
		Provider:         "openai",
		ModelFamily:      "responses",
		Refs:             []ContextRef{frag.Ref},
		MustKeepTogether: true,
		MustKeepRaw:      true,
		MustKeepOrder:    true,
		MustBeComplete:   true,
	}
	roundTripJSON(t, group, func(got ContinuityGroup) {
		if len(got.Refs) != 1 || !got.Refs[0].EqualIdentity(frag.Ref) || !got.MustKeepTogether || !got.MustBeComplete {
			t.Fatalf("continuity group mismatch after roundtrip: %#v", got)
		}
	})
}

func TestSchemaAndHashValidationRejectsSilentDrift(t *testing.T) {
	t.Parallel()

	if err := ValidateSchemaVersions([]SchemaVersion{{Name: SchemaContextFrag, Version: 999}}); err == nil {
		t.Fatal("unknown context frag schema version should fail validation")
	}
	if err := ValidateSchemaVersions([]SchemaVersion{{Name: "unknown_schema", Version: CurrentSchemaVersion}}); err == nil {
		t.Fatal("unknown schema name should fail validation")
	}
	if !UsesLockstepSchemaVersions() {
		t.Fatal("PR0 schema strategy should explicitly report lockstep schema versions")
	}
	for _, schema := range DefaultSchemaVersions() {
		if schema.Version != CurrentSchemaVersion {
			t.Fatalf("lockstep schema %q version = %d, want %d", schema.Name, schema.Version, CurrentSchemaVersion)
		}
	}
	if err := ValidateContextRef(ContextRef{
		Namespace: "history",
		ID:        "row-1",
		HashScope: "rendered_payload",
		Schema:    SchemaContextRef,
	}); err == nil {
		t.Fatal("non-canonical hash scope should fail even when content hash is empty")
	}
	wrongSchema := ContextEdit{
		EditID: "edit-wrong-schema",
		Slot:   SlotHistory,
		Op:     EditReplace,
		Schema: SchemaVersion{Name: SchemaContextRef, Version: CurrentSchemaVersion},
	}
	schemaConflicts := CheckEditPreconditions(wrongSchema, nil)
	if len(schemaConflicts) != 1 || schemaConflicts[0].Kind != ConflictInvalidSchema {
		t.Fatalf("wrong edit schema should produce invalid-schema conflict, got %#v", schemaConflicts)
	}

	ref := ContextRef{
		Namespace:   "history",
		ID:          "row-1",
		HashAlgo:    HashAlgoSHA256,
		HashScope:   HashScopeCanonicalFragment,
		ContentHash: "good",
		Schema:      SchemaContextRef,
	}
	edit := ContextEdit{
		EditID: "edit-1",
		Slot:   SlotHistory,
		Op:     EditReplace,
		Refs:   []ContextRef{ref},
		Preconditions: EditPreconditions{
			ExpectedHashes: map[string]string{ref.StableKey(): "bad"},
		},
		Schema: SchemaVersion{Name: SchemaContextEdit, Version: CurrentSchemaVersion},
	}
	conflicts := CheckEditPreconditions(edit, []ContextRef{ref})
	if len(conflicts) != 1 || conflicts[0].Kind != ConflictContentHashMismatch {
		t.Fatalf("expected one content-hash conflict, got %#v", conflicts)
	}

	sourceRef := ContextRef{
		Namespace:   "history",
		ID:          "row-source",
		HashAlgo:    HashAlgoSHA256,
		HashScope:   HashScopeSourcePayload,
		ContentHash: "source-good",
		Schema:      SchemaContextRef,
		Durability:  RefDurable,
	}
	sourceEdit := ContextEdit{
		EditID: "edit-source",
		Slot:   SlotHistory,
		Op:     EditReplace,
		Refs:   []ContextRef{sourceRef},
		Preconditions: EditPreconditions{
			ExpectedHashes: map[string]string{sourceRef.StableKey(): "source-good"},
		},
		Schema: SchemaVersion{Name: SchemaContextEdit, Version: CurrentSchemaVersion},
	}
	if conflicts := CheckEditPreconditions(sourceEdit, []ContextRef{sourceRef}); len(conflicts) != 0 {
		t.Fatalf("source payload expected hash should match, got %#v", conflicts)
	}
	sourceEdit.Preconditions.ExpectedHashes[sourceRef.StableKey()] = "source-bad"
	sourceConflicts := CheckEditPreconditions(sourceEdit, []ContextRef{sourceRef})
	if len(sourceConflicts) != 1 || sourceConflicts[0].Kind != ConflictContentHashMismatch {
		t.Fatalf("source payload mismatch should produce one hash conflict, got %#v", sourceConflicts)
	}

	missing := CheckEditPreconditions(edit, nil)
	if len(missing) != 1 || missing[0].Kind != ConflictMissingRef {
		t.Fatalf("expected one missing-ref conflict, got %#v", missing)
	}
}

func roundTripJSON[T any](t *testing.T, input T, check func(T)) {
	t.Helper()

	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal %T: %v", input, err)
	}
	var got T
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal %T: %v", input, err)
	}
	check(got)
}

func hasSchemaVersion(versions []SchemaVersion, name string, version int) bool {
	for _, got := range versions {
		if got.Name == name && got.Version == version {
			return true
		}
	}
	return false
}

func refForRenderedMessageText(frags []ContextFrag, text string) (ContextRef, bool) {
	for _, frag := range frags {
		for _, part := range frag.Parts {
			msg := partMessage(part)
			if msg == nil {
				continue
			}
			for _, content := range msg.Content {
				textPart, ok := content.(sdk.TextPart)
				if ok && textPart.Text == text {
					return frag.Ref, true
				}
			}
		}
	}
	return ContextRef{}, false
}

func renderedOutputContainsRef(outputs []RenderedOutputRef, ref ContextRef) bool {
	for _, output := range outputs {
		for _, got := range output.Refs {
			if got.EqualIdentity(ref) {
				return true
			}
		}
	}
	return false
}

func manifestHasWarning(manifest Manifest, code string) bool {
	return countManifestWarnings(manifest, code) > 0
}

func countManifestWarnings(manifest Manifest, code string) int {
	total := 0
	for _, warning := range manifest.ValidationWarnings {
		if warning.Code == code {
			total++
		}
	}
	return total
}

func containsJSONField(raw []byte, field string) bool {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	_, ok := value[field]
	return ok
}
