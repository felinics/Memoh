package contextview

import (
	"testing"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func toolCallMessageFrag(text string) contextfrag.ContextFrag {
	call := sdk.ToolCallPart{
		ToolCallID: "call-1",
		ToolName:   "web_search",
		Input:      map[string]any{"query": "memoh context usage panel design"},
	}
	return contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID:      "message.000",
		Message: sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: text}, call}},
		Kind:    contextfrag.KindConversationEvent,
		Slot:    contextfrag.SlotHistory,
	})
}

func TestTagFragmentsPricesWithSharedEstimator(t *testing.T) {
	t.Parallel()

	text := "Running the search now."
	frag := toolCallMessageFrag(text)

	tagged := tagFragments([]contextfrag.ContextFrag{frag}, IntentProfile{Intent: contextfrag.IntentRunConfigPreProvider})
	got := tagged[0].Tokens
	if want := contextfrag.ResolveFragTokens(frag); got != want {
		t.Fatalf("tagged tokens = %d, want shared estimator value %d", got, want)
	}
	if textOnly := len(text) / 4; got <= textOnly {
		t.Fatalf("tagged tokens = %d, must exceed text-only estimate %d", got, textOnly)
	}
}

func TestFragCharCountAddsToolPayloadToText(t *testing.T) {
	t.Parallel()

	text := "Running the search now."
	frag := toolCallMessageFrag(text)

	got := fragCharCount(frag)
	if textOnly := utf8.RuneCountInString(text); got <= textOnly {
		t.Fatalf("fragCharCount = %d, must exceed text-only count %d", got, textOnly)
	}
}
