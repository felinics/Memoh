package contextlimit

import (
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func TestTruncateStepToolResultStubsEachOversizedPartIndependently(t *testing.T) {
	t.Parallel()

	msg := sdk.Message{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{
		sdk.ToolResultPart{ToolCallID: "call-big", ToolName: "exec", Result: strings.Repeat("x", 1_000)},
		sdk.ToolResultPart{ToolCallID: "call-small", ToolName: "exec", Result: "ok: created id-42"},
	}}

	out, changed := TruncateStepToolResult(msg, 512)
	if !changed {
		t.Fatal("oversized part must be stubbed")
	}
	big, small := out.Content[0].(sdk.ToolResultPart), out.Content[1].(sdk.ToolResultPart)
	if text, _ := big.Result.(string); !strings.Contains(text, "pruned") {
		t.Fatalf("oversized part must carry the stub, got %v", big.Result)
	}
	if small.Result != "ok: created id-42" {
		t.Fatalf("small sibling must survive verbatim, got %v", small.Result)
	}
}

func TestTruncateStepToolResultPreservesErrorAndCacheMetadata(t *testing.T) {
	t.Parallel()

	cache := &sdk.CacheControl{}
	msg := sdk.Message{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{
		sdk.ToolResultPart{
			ToolCallID: "call-fail", ToolName: "exec",
			Result: strings.Repeat("stderr ", 200), IsError: true, CacheControl: cache,
		},
	}}

	out, changed := TruncateStepToolResult(msg, 512)
	if !changed {
		t.Fatal("oversized part must be stubbed")
	}
	stubbed := out.Content[0].(sdk.ToolResultPart)
	if !stubbed.IsError {
		t.Fatal("a failed result must not read as success after pruning")
	}
	if stubbed.CacheControl != cache {
		t.Fatal("cache control must survive pruning")
	}
}

func TestTruncateStepToolResultLeavesSmallPartsUntouched(t *testing.T) {
	t.Parallel()

	msg := sdk.Message{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{
		sdk.ToolResultPart{ToolCallID: "call-1", ToolName: "exec", Result: "small"},
	}}
	if _, changed := TruncateStepToolResult(msg, 512); changed {
		t.Fatal("under-threshold results must pass through unchanged")
	}
}
