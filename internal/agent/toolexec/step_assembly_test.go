package toolexec

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func TestBuildStepMessagesPreservesToolCallProviderMetadata(t *testing.T) {
	meta := sdk.ProviderMetadata{"google": {"thoughtSignature": "sig-1"}}
	msgs := BuildStepMessages("", nil, nil, []sdk.ToolCall{{
		ToolCallID:       "call-1",
		ToolName:         "lookup",
		Input:            sdk.ParseToolArguments(`{"q":"memoh"}`),
		ProviderMetadata: meta,
	}}, nil, nil)

	if len(msgs) != 1 || len(msgs[0].Content) != 1 {
		t.Fatalf("unexpected messages: %#v", msgs)
	}
	part, ok := msgs[0].Content[0].(sdk.ToolCallPart)
	if !ok {
		t.Fatalf("content part = %T, want ToolCallPart", msgs[0].Content[0])
	}
	if got := part.ProviderMetadata.Get("google", "thoughtSignature"); got != "sig-1" {
		t.Fatalf("thoughtSignature = %q, want sig-1", got)
	}
}
