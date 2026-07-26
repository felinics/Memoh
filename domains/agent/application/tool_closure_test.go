package application

import (
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/convert"
)

func TestRepairToolCallClosures_AppendsSyntheticToolResultForDanglingAssistantCall(t *testing.T) {
	t.Parallel()

	messages := convert.SDKMessagesToModelMessages([]sdk.Message{
		sdk.UserMessage("fetch this"),
		{
			Role: sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{
				sdk.ToolCallPart{
					ToolCallID: "web_fetch:10",
					ToolName:   "web_fetch",
					Input:      map[string]any{"url": "https://example.com"},
				},
			},
		},
		{
			Role:    sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{sdk.TextPart{Text: "interrupted"}},
		},
	})

	repaired := repairToolCallClosures(messages, syntheticToolClosureError)
	if len(repaired) != 4 {
		t.Fatalf("expected 4 messages after repair, got %d", len(repaired))
	}

	if repaired[2].Role != "tool" {
		t.Fatalf("expected synthetic tool message before trailing assistant, got role %q", repaired[2].Role)
	}

	results := extractToolResultParts(repaired[2])
	if len(results) != 1 {
		t.Fatalf("expected 1 tool result part, got %d", len(results))
	}
	if results[0].ToolCallID != "web_fetch:10" {
		t.Fatalf("expected tool call id web_fetch:10, got %q", results[0].ToolCallID)
	}
	if !results[0].IsError {
		t.Fatal("expected synthetic tool result to be marked as error")
	}
}

func TestRepairToolCallClosures_DropsOrphanToolMessage(t *testing.T) {
	t.Parallel()

	orphanTool := convert.SDKMessagesToModelMessages([]sdk.Message{
		sdk.ToolMessage(sdk.ToolResultPart{
			ToolCallID: "web_fetch:10",
			ToolName:   "web_fetch",
			Result:     "orphan",
		}),
	})[0]

	messages := []agentdomain.ModelMessage{
		{Role: "user", Content: agentdomain.NewTextContent("hello")},
		orphanTool,
		{Role: "assistant", Content: agentdomain.NewTextContent("done")},
	}

	repaired := repairToolCallClosures(messages, syntheticToolClosureError)
	if len(repaired) != 2 {
		t.Fatalf("expected orphan tool message to be removed, got %d messages", len(repaired))
	}
	if repaired[0].Role != "user" || repaired[1].Role != "assistant" {
		t.Fatalf("unexpected repaired role sequence: %q -> %q", repaired[0].Role, repaired[1].Role)
	}
}

func TestRepairToolCallClosures_PreservesValidAssistantToolPair(t *testing.T) {
	t.Parallel()

	messages := convert.SDKMessagesToModelMessages([]sdk.Message{
		{
			Role: sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{
				sdk.ToolCallPart{
					ToolCallID: "web_search:1",
					ToolName:   "web_search",
					Input:      map[string]any{"query": "memoh"},
				},
			},
		},
		sdk.ToolMessage(sdk.ToolResultPart{
			ToolCallID: "web_search:1",
			ToolName:   "web_search",
			Result:     map[string]any{"results": []any{}},
		}),
	})

	repaired := repairToolCallClosures(messages, syntheticToolClosureError)
	if len(repaired) != 2 {
		t.Fatalf("expected valid tool pair to be preserved, got %d messages", len(repaired))
	}
	results := extractToolResultParts(repaired[1])
	if len(results) != 1 || results[0].ToolCallID != "web_search:1" {
		t.Fatalf("unexpected repaired tool results: %#v", results)
	}
}
