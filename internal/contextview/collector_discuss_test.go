package contextview

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/chat/timeline"
)

func TestDiscussCollector_ComposedMessagesAreAuthoritative(t *testing.T) {
	t.Parallel()

	composed := []timeline.ContextMessage{
		{Role: "user", Content: "first user"},
		{Role: "assistant", Content: "assistant reply"},
		{
			Role:                 "user",
			Content:              "<summary>covered history</summary>",
			CompactionArtifactID: "artifact-1",
		},
		{
			Role:       "tool",
			Content:    "debug fallback",
			RawContent: json.RawMessage(`[{"type":"tool-result","toolCallId":"call-1","toolName":"lookup","result":{"answer":42}}]`),
		},
		{Role: "user", Content: "latest user"},
	}
	image := sdk.ImagePart{Image: "data:image/png;base64,abc", MediaType: "image/png"}
	frags := collectDiscussContext(t, DiscussContextConfig{
		ComposedMessages: composed,
		InlineImages:     []sdk.ImagePart{image},
	})

	assertDiscussIDs(t, frags, []string{
		"discuss.message.000",
		"discuss.message.001",
		"discuss.message.002",
		"discuss.message.003",
		"discuss.message.004",
	})
	for i, frag := range frags {
		if frag.Provenance.Source != discussContextSource ||
			frag.Provenance.SourceID != fmt.Sprintf("message.%03d", i) ||
			frag.Provenance.Collector != discussContextCollectorName ||
			frag.Provenance.Index != i {
			t.Fatalf("frag %d provenance = %+v", i, frag.Provenance)
		}
	}

	summary := frags[2]
	if summary.Kind != contextfrag.KindConversationSummary ||
		summary.Slot != contextfrag.SlotBeforeHistory ||
		summary.CacheClass != contextfrag.CacheDynamic ||
		summary.Trust != contextfrag.TrustSystem ||
		summary.Budget.Overflow != contextfrag.OverflowKeep {
		t.Fatalf("summary policy = kind %q slot %q cache %q trust %q overflow %q",
			summary.Kind, summary.Slot, summary.CacheClass, summary.Trust, summary.Budget.Overflow)
	}

	current := frags[len(frags)-1]
	if current.Kind != contextfrag.KindCurrentUserMessage ||
		current.Slot != contextfrag.SlotHistory ||
		current.Trust != contextfrag.TrustUser ||
		current.Budget.Overflow != contextfrag.OverflowKeep {
		t.Fatalf("current request policy = kind %q slot %q trust %q overflow %q",
			current.Kind, current.Slot, current.Trust, current.Budget.Overflow)
	}
	currentMessage := contextfrag.FragMessage(current)
	if currentMessage == nil || len(currentMessage.Content) != 2 {
		t.Fatalf("current message = %#v, want text plus image", currentMessage)
	}
	if got, ok := currentMessage.Content[1].(sdk.ImagePart); !ok || got.Image != image.Image {
		t.Fatalf("current image = %#v, want %#v", currentMessage.Content[1], image)
	}
	if frags[0].Kind == contextfrag.KindCurrentUserMessage || frags[2].Kind == contextfrag.KindCurrentUserMessage {
		t.Fatalf("only the latest non-artifact user may be current: %#v", frags)
	}

	toolMessage := contextfrag.FragMessage(frags[3])
	if toolMessage == nil || toolMessage.Role != sdk.MessageRoleTool || len(toolMessage.Content) != 1 {
		t.Fatalf("structured tool message = %#v", toolMessage)
	}
}

func TestDiscussCollector_NonNilEmptyComposedMessagesAreAuthoritative(t *testing.T) {
	t.Parallel()

	frags := collectDiscussContext(t, DiscussContextConfig{
		ComposedMessages: []timeline.ContextMessage{},
		InlineImages: []sdk.ImagePart{{
			Image:     "data:image/png;base64,ignored-without-user",
			MediaType: "image/png",
		}},
	})
	if frags == nil || len(frags) != 0 {
		t.Fatalf("frags = %#v, want authoritative non-nil empty result", frags)
	}
}

func collectDiscussContext(t *testing.T, cfg DiscussContextConfig) []contextfrag.ContextFrag {
	t.Helper()
	frags, err := (&DiscussContextCollector{}).Collect(context.Background(), CollectRequest{
		Scope:  contextfrag.Scope{BotID: "bot-1", SessionID: "s1"},
		Intent: contextfrag.IntentDiscussReply,
		Config: cfg,
	})
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}
	return frags
}

func assertDiscussIDs(t *testing.T, frags []contextfrag.ContextFrag, want []string) {
	t.Helper()
	if len(frags) != len(want) {
		t.Fatalf("frag count = %d, want %d", len(frags), len(want))
	}
	for i, id := range want {
		if frags[i].ID != id {
			t.Fatalf("frag %d ID = %q, want %q", i, frags[i].ID, id)
		}
	}
}
