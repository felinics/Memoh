package contextfrag

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func TestIsBackgroundSummaryCarrier(t *testing.T) {
	t.Parallel()

	prefixed := BackgroundSummaryMessagePrefix + "Currently running background tasks:\n- [task-1] build"
	cases := []struct {
		name string
		msg  sdk.Message
		want bool
	}{
		{"carrier", sdk.UserMessage(prefixed), true},
		{"assistant role", sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: prefixed}}}, false},
		{"no parts", sdk.Message{Role: sdk.MessageRoleUser}, false},
		{"extra part", sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: prefixed}, sdk.TextPart{Text: "more"}}}, false},
		{"non-text part", sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.ImagePart{Image: "data:image/png;base64,abc"}}}, false},
		{"no prefix", sdk.UserMessage("Currently running background tasks"), false},
		{"cache control", sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: prefixed, CacheControl: &sdk.CacheControl{Type: "ephemeral"}}}}, false},
		{"provider metadata", sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: prefixed, ProviderMetadata: map[string]any{"k": "v"}}}}, false},
	}
	for _, tc := range cases {
		if got := IsBackgroundSummaryCarrier(tc.msg); got != tc.want {
			t.Fatalf("%s: IsBackgroundSummaryCarrier() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDefaultToolExchangePolicy(t *testing.T) {
	t.Parallel()

	got := DefaultToolExchangePolicy()
	if got.MinMessages != 10 {
		t.Fatalf("MinMessages = %d, want 10", got.MinMessages)
	}

	other := DefaultToolExchangePolicy()
	other.MinMessages = 99
	if got.MinMessages == other.MinMessages {
		t.Fatal("DefaultToolExchangePolicy must return a fresh pointer per call, not a shared one")
	}
}
