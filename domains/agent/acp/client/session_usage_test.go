package client

import (
	"testing"

	acp "github.com/coder/acp-go-sdk"
	sdk "github.com/memohai/twilight-ai/sdk"
)

func TestPromptUsageFromACPMapsTokenDetails(t *testing.T) {
	cacheRead := 3
	cacheWrite := 2
	thought := 5

	got := promptUsageFromACP(&acp.Usage{
		InputTokens:       10,
		OutputTokens:      7,
		TotalTokens:       17,
		CachedReadTokens:  &cacheRead,
		CachedWriteTokens: &cacheWrite,
		ThoughtTokens:     &thought,
	})

	if got == nil {
		t.Fatal("usage = nil")
	}
	if got.InputTokens != 10 || got.OutputTokens != 7 || got.TotalTokens != 17 {
		t.Fatalf("usage totals = %+v", got)
	}
	if got.CachedInputTokens != 3 || got.InputTokenDetails.CacheReadTokens != 3 || got.InputTokenDetails.CacheWriteTokens != 2 {
		t.Fatalf("input token details = %+v", got)
	}
	if got.ReasoningTokens != 5 || got.OutputTokenDetails.ReasoningTokens != 5 {
		t.Fatalf("reasoning token details = %+v", got)
	}
}

func TestAttachUsageToLastAssistant(t *testing.T) {
	usage := &sdk.Usage{InputTokens: 10, OutputTokens: 7, TotalTokens: 17}
	output := []sdk.Message{
		sdk.UserMessage("question"),
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: "first"}}},
		{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{sdk.ToolResultPart{ToolCallID: "call-1", ToolName: "exec", Result: "ok"}}},
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: "final"}}},
	}

	got := attachUsageToLastAssistant(output, usage)

	if got[1].Usage != nil {
		t.Fatalf("first assistant usage = %+v, want nil", got[1].Usage)
	}
	if got[3].Usage != usage {
		t.Fatalf("final assistant usage = %+v, want mapped usage", got[3].Usage)
	}
}
