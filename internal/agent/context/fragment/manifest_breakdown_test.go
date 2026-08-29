package contextfrag

import (
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func breakdownFixtureFrags() []ContextFrag {
	sysA := TextFrag(TextFragInput{
		ID: "system.prompt", Kind: KindSystemPrompt, Slot: SlotSystem,
		Text: strings.Repeat("a", 40),
	})
	sysB := TextFrag(TextFragInput{
		ID: "system.prompt.tail", Kind: KindSystemPrompt, Slot: SlotSystem,
		Text: strings.Repeat("b", 20),
	})
	hist := MessageFrag(MessageFragInput{
		ID:      "history.db_message.m1",
		Message: sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: "hi"}}},
		Kind:    KindConversationEvent,
		Slot:    SlotHistory,
	})
	hist.TokenEstimate = 123
	img := ImageFrag("current_user.images", []sdk.ImagePart{{Image: "data:image/png;base64,AAAA", MediaType: "image/png"}}, Scope{}, "run_config")
	return []ContextFrag{sysA, sysB, hist, img}
}

func TestBuildManifestItemsCarryTokenEstimates(t *testing.T) {
	t.Parallel()

	manifest := BuildManifest(breakdownFixtureFrags())
	if len(manifest.Items) != 4 {
		t.Fatalf("items = %d, want 4", len(manifest.Items))
	}
	wantByID := map[string]int{
		"system.prompt":         10,
		"system.prompt.tail":    5,
		"history.db_message.m1": 123,
		"current_user.images":   EstimateImageTokens,
	}
	for _, item := range manifest.Items {
		if want, ok := wantByID[item.ID]; ok && item.TokenEstimate != want {
			t.Fatalf("item %s token estimate = %d, want %d", item.ID, item.TokenEstimate, want)
		}
	}
	if want := 10 + 5 + 123 + EstimateImageTokens; manifest.Counts.TokenEstimate != want {
		t.Fatalf("counts token estimate = %d, want %d", manifest.Counts.TokenEstimate, want)
	}
}

func TestBuildManifestBreakdownAggregatesByKind(t *testing.T) {
	t.Parallel()

	manifest := BuildManifest(breakdownFixtureFrags())
	want := []KindBreakdown{
		{Kind: KindNativeImage, Fragments: 1, TokenEstimate: EstimateImageTokens, Images: 1},
		{Kind: KindConversationEvent, Fragments: 1, TokenEstimate: 123, TextBytes: 2},
		{Kind: KindSystemPrompt, Fragments: 2, TokenEstimate: 15, TextBytes: 60},
	}
	if len(manifest.Breakdown) != len(want) {
		t.Fatalf("breakdown = %+v, want %+v", manifest.Breakdown, want)
	}
	for i, entry := range want {
		if manifest.Breakdown[i] != entry {
			t.Fatalf("breakdown[%d] = %+v, want %+v", i, manifest.Breakdown[i], entry)
		}
	}
}

func TestBuildManifestBreakdownOrderIsDeterministicOnTies(t *testing.T) {
	t.Parallel()

	a := TextFrag(TextFragInput{ID: "a", Kind: KindToolUsage, Slot: SlotSystem, Text: strings.Repeat("x", 40)})
	b := TextFrag(TextFragInput{ID: "b", Kind: KindBotIdentity, Slot: SlotSystem, Text: strings.Repeat("y", 40)})
	manifest := BuildManifest([]ContextFrag{a, b})
	if manifest.Breakdown[0].Kind != KindBotIdentity || manifest.Breakdown[1].Kind != KindToolUsage {
		t.Fatalf("tie order = %+v, want bot_identity before tool_usage", manifest.Breakdown)
	}
}

func TestBuildManifestTrustBreakdownMeasuresExposure(t *testing.T) {
	t.Parallel()

	frags := []ContextFrag{
		TextFrag(TextFragInput{ID: "system.prompt", Kind: KindSystemPrompt, Slot: SlotSystem, Trust: TrustSystem, Text: strings.Repeat("s", 40)}),
		MessageFrag(MessageFragInput{
			ID: "history.db_message.m1", Kind: KindConversationEvent, Slot: SlotHistory, Trust: TrustExternal,
			Message: sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: strings.Repeat("e", 80)}}},
		}),
		MessageFrag(MessageFragInput{
			ID: "history.db_message.m2", Kind: KindConversationEvent, Slot: SlotHistory, Trust: TrustWorkspace,
			Message: sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: strings.Repeat("w", 40)}}},
		}),
	}
	manifest := BuildManifest(frags)
	want := []TrustBreakdown{
		{Trust: TrustExternal, Fragments: 1, TokenEstimate: 20, TextBytes: 80},
		{Trust: TrustSystem, Fragments: 1, TokenEstimate: 10, TextBytes: 40},
		{Trust: TrustWorkspace, Fragments: 1, TokenEstimate: 10, TextBytes: 40},
	}
	if len(manifest.TrustBreakdown) != len(want) {
		t.Fatalf("trust breakdown = %+v, want %+v", manifest.TrustBreakdown, want)
	}
	for i := range want {
		if manifest.TrustBreakdown[i] != want[i] {
			t.Fatalf("trust breakdown[%d] = %+v, want %+v", i, manifest.TrustBreakdown[i], want[i])
		}
	}
}
