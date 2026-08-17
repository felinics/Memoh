package contextview

import (
	"testing"
	"unicode/utf8"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
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

func TestFragTokenEstimateAddsToolPayloadToText(t *testing.T) {
	t.Parallel()

	text := "Running the search now."
	frag := toolCallMessageFrag(text)

	got := fragTokenEstimate(frag)
	if want := contextfrag.ResolveFragTokens(frag); got != want {
		t.Fatalf("fragTokenEstimate = %d, want shared estimator value %d", got, want)
	}
	if textOnly := len(text) / 4; got <= textOnly {
		t.Fatalf("fragTokenEstimate = %d, must exceed text-only estimate %d", got, textOnly)
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
