package contextfrag

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func TestTokensFromBytes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   int
		want int
	}{
		{name: "zero", in: 0, want: 0},
		{name: "negative", in: -8, want: 0},
		{name: "below one token", in: 3, want: 0},
		{name: "exact", in: 8, want: 2},
		{name: "floor", in: 7, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := TokensFromBytes(tc.in); got != tc.want {
				t.Fatalf("TokensFromBytes(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestProviderBudgetTokensFromBytesUsesCeilingAndSafetyMargin(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   int
		want int
	}{
		{in: -1, want: 0},
		{in: 0, want: 0},
		{in: 1, want: 1},
		{in: 3, want: 1},
		{in: 4, want: 1},
		{in: 7, want: 2},
		{in: 8, want: 2},
		{in: 16, want: 5},
		{in: 4096, want: 1280},
	}
	for _, tc := range cases {
		if got := ProviderBudgetTokensFromBytes(tc.in); got != tc.want {
			t.Fatalf("ProviderBudgetTokensFromBytes(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}

	if got := TokensFromBytes(3); got != 0 {
		t.Fatalf("TokensFromBytes(3) = %d, want legacy floor estimate 0", got)
	}
}

func TestResolveProviderBudgetFragTokensKeepsGreaterEstimate(t *testing.T) {
	t.Parallel()

	frag := TextFrag(TextFragInput{
		ID:   "current",
		Kind: KindCurrentUserMessage,
		Slot: SlotCurrentUser,
		Text: "abcdefghijklmnop",
	})
	frag.TokenEstimate = 1
	if got := ResolveProviderBudgetFragTokens(frag); got != 5 {
		t.Fatalf("ResolveProviderBudgetFragTokens(16 bytes) = %d, want conservative byte estimate 5", got)
	}

	frag.TokenEstimate = 99
	if got := ResolveProviderBudgetFragTokens(frag); got != 99 {
		t.Fatalf("ResolveProviderBudgetFragTokens(authoritative) = %d, want 99", got)
	}
}

func TestEstimateSDKMessageTokensTextOnly(t *testing.T) {
	t.Parallel()

	text := "hello world, this is a test!"
	msg := sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: text}}}
	if got, want := EstimateSDKMessageTokens(msg), len(text)/4; got != want {
		t.Fatalf("EstimateSDKMessageTokens = %d, want %d", got, want)
	}
}

func TestEstimateSDKMessageTokensEmpty(t *testing.T) {
	t.Parallel()

	if got := EstimateSDKMessageTokens(sdk.Message{Role: sdk.MessageRoleUser}); got != 0 {
		t.Fatalf("EstimateSDKMessageTokens(empty) = %d, want 0", got)
	}
}

func TestEstimateSDKMessageTokensCountsUnicodeBytes(t *testing.T) {
	t.Parallel()

	msg := sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: "你好世界"}}}
	if got := EstimateSDKMessageTokens(msg); got != 3 {
		t.Fatalf("EstimateSDKMessageTokens(CJK 12 bytes) = %d, want 3", got)
	}
}

// The estimate must be additive across parts: an assistant message carrying
// both text and a tool call counts the tool call payload too, instead of
// discarding it because text is present.
func TestEstimateSDKMessageTokensAddsToolCallToText(t *testing.T) {
	t.Parallel()

	text := "Let me check that file now."
	call := sdk.ToolCallPart{
		ToolCallID: "call-1",
		ToolName:   "read_file",
		Input:      map[string]any{"path": "/data/projects/memoh/internal/agent/runtime/native/agent.go"},
	}
	msg := sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: text}, call}}

	callBytes, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	want := (len(text) + len(callBytes)) / 4
	got := EstimateSDKMessageTokens(msg)
	if got != want {
		t.Fatalf("EstimateSDKMessageTokens = %d, want %d", got, want)
	}
	if textOnly := len(text) / 4; got <= textOnly {
		t.Fatalf("EstimateSDKMessageTokens = %d, must exceed text-only estimate %d", got, textOnly)
	}
}

func TestEstimateSDKMessageTokensCountsToolResult(t *testing.T) {
	t.Parallel()

	result := sdk.ToolResultPart{
		ToolCallID: "call-1",
		ToolName:   "read_file",
		Result:     map[string]any{"content": "package native\n\nfunc main() {}\n"},
	}
	msg := sdk.Message{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{result}}

	resultBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := EstimateSDKMessageTokens(msg), len(resultBytes)/4; got != want || got == 0 {
		t.Fatalf("EstimateSDKMessageTokens = %d, want %d (nonzero)", got, want)
	}
}

func TestEstimateSDKMessageTokensCountsReasoning(t *testing.T) {
	t.Parallel()

	reasoning := "The user wants a summary of the diff, so read it first."
	msg := sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ReasoningPart{Text: reasoning}}}
	if got, want := EstimateSDKMessageTokens(msg), len(reasoning)/4; got != want {
		t.Fatalf("EstimateSDKMessageTokens = %d, want %d", got, want)
	}
}

// Byte totals are summed first and divided once, so multi-part messages do
// not lose up to three bytes of remainder per part.
func TestEstimateSDKMessageTokensSumsBeforeDividing(t *testing.T) {
	t.Parallel()

	msg := sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{
		sdk.TextPart{Text: "abcdef"},
		sdk.TextPart{Text: "ghijkl"},
	}}
	if got := EstimateSDKMessageTokens(msg); got != 3 {
		t.Fatalf("EstimateSDKMessageTokens(6+6 bytes) = %d, want 3", got)
	}
}

func TestEstimateFragTokensTextPart(t *testing.T) {
	t.Parallel()

	frag := TextFrag(TextFragInput{
		ID:   "system.prompt",
		Kind: KindSystemPrompt,
		Slot: SlotSystem,
		Text: "You are an AI agent running inside a private Memoh workspace.",
	})
	if got, want := EstimateFragTokens(frag), len(frag.Parts[0].Text)/4; got != want {
		t.Fatalf("EstimateFragTokens = %d, want %d", got, want)
	}
}

func TestEstimateFragTokensSDKMessagePart(t *testing.T) {
	t.Parallel()

	text := "What changed in the last release?"
	frag := MessageFrag(MessageFragInput{
		ID:      "message.000",
		Message: sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: text}}},
		Kind:    KindConversationEvent,
		Slot:    SlotHistory,
	})
	if got, want := EstimateFragTokens(frag), len(text)/4; got != want {
		t.Fatalf("EstimateFragTokens = %d, want %d", got, want)
	}
}

// Images carry a flat estimate: never their base64 payload bytes (which
// overstate provider cost by orders of magnitude) and never zero (which
// would make image-heavy history invisible to budget pressure).
func TestEstimateFragTokensFlatImageEstimate(t *testing.T) {
	t.Parallel()

	bulky := strings.Repeat("A", 40000)
	frag := ImageFrag("current_user.images", []sdk.ImagePart{{Image: "data:image/png;base64," + bulky, MediaType: "image/png"}}, Scope{}, "run_config")
	if got := EstimateFragTokens(frag); got != EstimateImageTokens {
		t.Fatalf("EstimateFragTokens(image) = %d, want %d", got, EstimateImageTokens)
	}
}

func TestEstimateSDKMessageTokensFlatImageEstimate(t *testing.T) {
	t.Parallel()

	bulky := strings.Repeat("A", 40000)
	msg := sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{
		sdk.TextPart{Text: "abcdefgh"},
		sdk.ImagePart{Image: "data:image/png;base64," + bulky, MediaType: "image/png"},
		sdk.ImagePart{Image: "data:image/png;base64," + bulky, MediaType: "image/png"},
	}}
	if got, want := EstimateSDKMessageTokens(msg), 2+2*EstimateImageTokens; got != want {
		t.Fatalf("EstimateSDKMessageTokens = %d, want %d", got, want)
	}
}

func TestEstimateFragTokensIgnoresPresetEstimate(t *testing.T) {
	t.Parallel()

	frag := TextFrag(TextFragInput{ID: "f", Kind: KindSystemPrompt, Slot: SlotSystem, Text: "abcdefgh"})
	frag.TokenEstimate = 999
	if got := EstimateFragTokens(frag); got != 2 {
		t.Fatalf("EstimateFragTokens = %d, want 2 (computed from parts)", got)
	}
}

func TestResolveFragTokensPrefersPresetEstimate(t *testing.T) {
	t.Parallel()

	frag := TextFrag(TextFragInput{ID: "f", Kind: KindSystemPrompt, Slot: SlotSystem, Text: "abcdefgh"})
	frag.TokenEstimate = 999
	if got := ResolveFragTokens(frag); got != 999 {
		t.Fatalf("ResolveFragTokens = %d, want 999", got)
	}
	frag.TokenEstimate = 0
	if got := ResolveFragTokens(frag); got != 2 {
		t.Fatalf("ResolveFragTokens = %d, want 2 (fallback to computed)", got)
	}
}

func TestProviderEnvelopeTokensPricesInlineImagesFlat(t *testing.T) {
	t.Parallel()

	photo := sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{
		sdk.TextPart{Text: "what is in this photo?"},
		sdk.ImagePart{Image: "data:image/jpeg;base64," + strings.Repeat("A", 400_000), MediaType: "image/jpeg"},
	}}
	got := ProviderEnvelopeTokens("", []sdk.Message{photo}, nil)
	want := ResolveProviderBudgetFragTokens(MessageFrag(MessageFragInput{Message: photo}))
	if got != want {
		t.Fatalf("ProviderEnvelopeTokens(photo) = %d, want selection estimate %d", got, want)
	}
	if got > 2*EstimateImageTokens {
		t.Fatalf("ProviderEnvelopeTokens(photo) = %d, want flat image pricing near %d", got, EstimateImageTokens)
	}
}

func TestProviderEnvelopeTokensSumsSystemMessagesAndTools(t *testing.T) {
	t.Parallel()

	system := strings.Repeat("s", 400)
	messages := []sdk.Message{
		sdk.UserMessage(strings.Repeat("u", 800)),
		{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{sdk.ToolResultPart{
			ToolCallID: "call-1", ToolName: "exec", Result: strings.Repeat("r", 1200),
		}}},
	}
	tools := []sdk.Tool{{
		Name:        "exec",
		Description: "Execute a bounded command.",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}},
	}}

	if got := ProviderEnvelopeTokens(system, messages[:1], nil); got != 125+250 {
		t.Fatalf("ProviderEnvelopeTokens(system+user) = %d, want 375 (400 and 800 bytes at ceil/4 x 1.25)", got)
	}
	want := 375 + ResolveProviderBudgetFragTokens(MessageFrag(MessageFragInput{Message: messages[1]})) +
		ProviderToolDefTokens(ToolDefAccountingFor("native", tools[0]))
	if got := ProviderEnvelopeTokens(system, messages, tools); got != want {
		t.Fatalf("ProviderEnvelopeTokens = %d, want %d", got, want)
	}
	if got := ProviderEnvelopeTokens("", nil, nil); got != 0 {
		t.Fatalf("ProviderEnvelopeTokens(empty) = %d, want 0", got)
	}
}
