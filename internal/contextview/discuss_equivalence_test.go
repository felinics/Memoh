package contextview

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/chat/timeline"
)

func TestDiscussEquivalence_BasicRCAndTR(t *testing.T) {
	t.Parallel()

	assertDiscussEquivalent(t, discussLegacyInput{
		system: "Discuss system prompt.",
		rc: timeline.RenderedContext{
			renderedTextSegment(100, "first rc"),
			renderedTextSegment(300, "second rc"),
		},
		trs: []timeline.TurnResponseEntry{{
			RequestedAtMs: 200,
			Role:          "assistant",
			Content:       "assistant turn",
		}},
	})
}

func TestDiscussEquivalence_ConsecutiveRCMerged(t *testing.T) {
	t.Parallel()

	assertDiscussEquivalent(t, discussLegacyInput{
		rc: timeline.RenderedContext{
			renderedTextSegment(100, "one"),
			renderedTextSegment(200, "two"),
			renderedTextSegment(300, "three"),
		},
	})
}

func TestDiscussEquivalence_CompactSummaryPrepended(t *testing.T) {
	t.Parallel()

	assertDiscussEquivalent(t, discussLegacyInput{
		summary: "older context summary",
		rc:      timeline.RenderedContext{renderedTextSegment(100, "live context")},
	})
}

func TestDiscussEquivalence_TRWithRawContent(t *testing.T) {
	t.Parallel()

	assertDiscussEquivalent(t, discussLegacyInput{
		rc: timeline.RenderedContext{renderedTextSegment(200, "next question")},
		trs: []timeline.TurnResponseEntry{{
			RequestedAtMs: 100,
			Role:          "tool",
			Content:       "debug text",
			RawContent:    json.RawMessage(`[{"type":"tool-result","toolCallId":"call-1","toolName":"lookup","result":{"answer":42}}]`),
		}},
	})
}

func TestDiscussEquivalence_EmptyInput(t *testing.T) {
	t.Parallel()

	assertDiscussEquivalent(t, discussLegacyInput{})
}

func TestDiscussEquivalence_RCBeforeTROnEqualTimestamp(t *testing.T) {
	t.Parallel()

	assertDiscussEquivalent(t, discussLegacyInput{
		rc: timeline.RenderedContext{
			renderedTextSegment(100, "same-time rc"),
		},
		trs: []timeline.TurnResponseEntry{{
			RequestedAtMs: 100,
			Role:          "assistant",
			Content:       "same-time tr",
		}},
	})
}

func TestDiscussSelector_ProfileMustKeepSystemAndCurrentUser(t *testing.T) {
	t.Parallel()

	profile := (&FragmentSelector{}).ProfileFor(contextfrag.IntentDiscussReply)

	if profile.Intent != contextfrag.IntentDiscussReply {
		t.Fatalf("Intent = %q, want %q", profile.Intent, contextfrag.IntentDiscussReply)
	}
	if !slotInProfile(profile, contextfrag.SlotSystem) {
		t.Fatalf("MustKeepSlots = %#v, want system", profile.MustKeepSlots)
	}
	if !slotInProfile(profile, contextfrag.SlotCurrentUser) {
		t.Fatalf("MustKeepSlots = %#v, want current_user", profile.MustKeepSlots)
	}
}

func TestDiscussSelector_BudgetedSelectionDropsCanDropHistory(t *testing.T) {
	t.Parallel()

	frags := []contextfrag.ContextFrag{
		messageFrag("old-user", sdk.UserMessage("old question")),
		messageFrag("old-assistant", sdk.AssistantMessage("old answer")),
		messageFrag("latest", sdk.UserMessage("latest question")),
	}
	selector := &FragmentSelector{}
	profile := selector.ProfileFor(contextfrag.IntentDiscussReply)

	result := selector.Select(frags, profile, BudgetEnvelope{MaxTokens: 1})

	assertSelectedIDs(t, result, []string{"latest"})
	assertDroppedReason(t, result, "old-user", budgetDropReasonUntiered)
	assertDroppedReason(t, result, "old-assistant", budgetDropReasonUntiered)
}

func TestDiscussSelector_BudgetedSelectionKeepsAllWhenWithinBudget(t *testing.T) {
	t.Parallel()

	frags := []contextfrag.ContextFrag{
		messageFrag("old-user", sdk.UserMessage("old question")),
		messageFrag("old-assistant", sdk.AssistantMessage("old answer")),
		messageFrag("latest", sdk.UserMessage("latest question")),
	}
	selector := &FragmentSelector{}
	profile := selector.ProfileFor(contextfrag.IntentDiscussReply)

	result := selector.Select(frags, profile, BudgetEnvelope{MaxTokens: 1000})

	assertSelectedIDs(t, result, []string{"old-user", "old-assistant", "latest"})
	if len(result.Dropped) != 0 {
		t.Fatalf("dropped = %#v, want none", fragIDs(result.Dropped))
	}
}

func TestSelector_ProviderBudgetedIntentDropsCanDropHistory(t *testing.T) {
	t.Parallel()

	frags := []contextfrag.ContextFrag{
		messageFrag("old-user", sdk.UserMessage("old question")),
		messageFrag("old-assistant", sdk.AssistantMessage("old answer")),
		messageFrag("latest", sdk.UserMessage("latest question")),
	}
	selector := &FragmentSelector{}
	profile := selector.ProfileFor(contextfrag.IntentRunConfigPreProvider)

	result := selector.Select(frags, profile, BudgetEnvelope{MaxTokens: 1})

	assertSelectedIDs(t, result, []string{"latest"})
	assertDroppedReason(t, result, "old-user", budgetDropReasonUntiered)
	assertDroppedReason(t, result, "old-assistant", budgetDropReasonUntiered)
}

type discussLegacyInput struct {
	system  string
	rc      timeline.RenderedContext
	trs     []timeline.TurnResponseEntry
	summary string
}

func assertDiscussEquivalent(t *testing.T, input discussLegacyInput) {
	t.Helper()
	scope := contextfrag.Scope{BotID: "bot-1", SessionID: "s1"}
	composedMessages := composeDiscussMessages(input.rc, input.trs, input.summary)
	flatMessages := legacyContextMessagesToSDK(composedMessages)
	legacyRendered, err := applyProviderRunConfig(context.Background(), nil, agentpkg.RunConfig{
		System:                   input.system,
		Messages:                 flatMessages,
		ContextQueryMaterialized: true,
		ContextScope:             scope,
	})
	if err != nil {
		t.Fatalf("legacy applyProviderRunConfig() error: %v", err)
	}
	typedFrags, err := (&DiscussSDKContextBuilder{}).CollectDiscussSourceFrags(
		context.Background(),
		scope,
		input.system,
		DiscussContextInput{ComposedMessages: composedMessages},
	)
	if err != nil {
		t.Fatalf("CollectDiscussSourceFrags() error: %v", err)
	}
	typedRendered, err := applyProviderRunConfig(context.Background(), nil, agentpkg.RunConfig{
		System:                   input.system,
		Messages:                 flatMessages,
		ContextSourceFrags:       typedFrags,
		ContextQueryMaterialized: true,
		ContextScope:             scope,
	})
	if err != nil {
		t.Fatalf("typed applyProviderRunConfig() error: %v", err)
	}
	for _, mutation := range typedRendered.ContextManifest.Mutations.Records() {
		if mutation.Kind == contextfrag.MutationContextViewFallback {
			t.Fatalf("typed provider path fell back to the flat oracle: %+v", mutation)
		}
	}

	if typedRendered.System != legacyRendered.System {
		t.Fatalf("typed System = %q, want legacy %q", typedRendered.System, legacyRendered.System)
	}
	assertMessagesEqual(t, typedRendered.Messages, legacyRendered.Messages)
}

func composeDiscussMessages(rc timeline.RenderedContext, trs []timeline.TurnResponseEntry, summary string) []timeline.ContextMessage {
	var artifacts []timeline.CompactionArtifact
	if strings.TrimSpace(summary) != "" {
		artifacts = []timeline.CompactionArtifact{{ID: "equivalence-artifact", Summary: summary}}
	}
	composed := timeline.ComposeContextWithArtifacts(rc, trs, artifacts)
	if composed == nil {
		return make([]timeline.ContextMessage, 0)
	}
	return composed.Messages
}

func legacyContextMessagesToSDK(messages []timeline.ContextMessage) []sdk.Message {
	if len(messages) == 0 {
		return nil
	}
	result := make([]sdk.Message, 0, len(messages))
	for _, message := range messages {
		if len(message.RawContent) > 0 {
			raw, err := json.Marshal(struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}{
				Role:    message.Role,
				Content: message.RawContent,
			})
			if err == nil {
				var msg sdk.Message
				if json.Unmarshal(raw, &msg) == nil {
					result = append(result, msg)
					continue
				}
			}
		}
		switch message.Role {
		case "assistant":
			result = append(result, sdk.AssistantMessage(message.Content))
		case "user", "tool":
			result = append(result, sdk.UserMessage(message.Content))
		default:
			result = append(result, sdk.UserMessage(message.Content))
		}
	}
	return result
}

func renderedTextSegment(atMs int64, text string) timeline.RenderedSegment {
	return timeline.RenderedSegment{
		ReceivedAtMs: atMs,
		Content:      []timeline.RenderedContentPiece{{Type: "text", Text: text}},
	}
}

func slotInProfile(profile IntentProfile, slot contextfrag.Slot) bool {
	if slotInMustKeepSlots(profile, slot) {
		return true
	}
	return profile.MustKeepFrag != nil && profile.MustKeepFrag(contextfrag.ContextFrag{Slot: slot})
}
