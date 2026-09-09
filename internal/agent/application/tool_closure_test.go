package application

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func TestRepairToolCallClosures_AppendsSyntheticToolResultForDanglingAssistantCall(t *testing.T) {
	t.Parallel()

	messages := sdkMessagesToModelMessages([]sdk.Message{
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

	orphanTool := sdkMessagesToModelMessages([]sdk.Message{
		sdk.ToolMessage(sdk.ToolResultPart{
			ToolCallID: "web_fetch:10",
			ToolName:   "web_fetch",
			Result:     "orphan",
		}),
	})[0]

	messages := []ModelMessage{
		{Role: "user", Content: newTextContent("hello")},
		orphanTool,
		{Role: "assistant", Content: newTextContent("done")},
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

	messages := sdkMessagesToModelMessages([]sdk.Message{
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

func projectedAskUserCall(id string) sdk.Message {
	return sdk.Message{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{
			sdk.ToolCallPart{
				ToolCallID: id,
				ToolName:   "ask_user",
				Input:      map[string]any{"questions": []any{}},
				ProviderMetadata: map[string]any{
					"user_input": map[string]any{"user_input_id": "input-1", "status": "pending"},
				},
			},
		},
	}
}

func TestRepairToolCallClosures_DoesNotMatchReusedIDAcrossUserTurns(t *testing.T) {
	t.Parallel()

	messages := sdkMessagesToModelMessages([]sdk.Message{
		sdk.UserMessage("first turn"),
		projectedAskUserCall("ask-1"),
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: "first turn ended"}}},
		sdk.UserMessage("second turn"),
		// Older rows can contain both pending and terminal projections. They
		// still collapse within this turn, but must not match the first turn.
		projectedAskUserCall("ask-1"),
		projectedAskUserCall("ask-1"),
		sdk.ToolMessage(sdk.ToolResultPart{
			ToolCallID: "ask-1",
			ToolName:   "ask_user",
			Result:     map[string]any{"status": "submitted"},
		}),
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: "second turn done"}}},
	})

	repaired := repairToolCallClosures(messages, syntheticToolClosureError)
	callIndexes := make([]int, 0, 2)
	for index, msg := range repaired {
		for _, call := range extractAssistantToolCallParts(msg) {
			if call.ToolCallID == "ask-1" {
				callIndexes = append(callIndexes, index)
			}
		}
	}
	if len(callIndexes) != 2 {
		t.Fatalf("ask-1 call count = %d, want one per turn: %#v", len(callIndexes), repaired)
	}
	for index, callIndex := range callIndexes {
		if callIndex+1 >= len(repaired) {
			t.Fatalf("ask-1 call at %d has no following result", callIndex)
		}
		results := extractToolResultParts(repaired[callIndex+1])
		if len(results) != 1 || results[0].ToolCallID != "ask-1" {
			t.Fatalf("result after ask-1 call %d = %#v", index+1, repaired[callIndex+1])
		}
		if got, want := results[0].IsError, index == 0; got != want {
			t.Fatalf("ask-1 result %d IsError = %v, want %v", index+1, got, want)
		}
	}
}

func TestRepairToolCallClosures_UsesResolvedUserInputResult(t *testing.T) {
	t.Parallel()

	call := projectedAskUserCall("ask-1")
	part := call.Content[0].(sdk.ToolCallPart)
	part.ProviderMetadata["user_input"] = map[string]any{
		"status":  "submitted",
		"answers": []any{map[string]any{"question_id": "q1"}},
	}
	call.Content[0] = part
	messages := sdkMessagesToModelMessages([]sdk.Message{
		call,
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: "done"}}},
	})

	repaired := repairToolCallClosures(messages, syntheticToolClosureError)
	results := extractToolResultParts(repaired[1])
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("resolved ask_user result = %#v", results)
	}
	result, ok := results[0].Result.(map[string]any)
	if !ok || result["status"] != "submitted" || result["answers"] == nil {
		t.Fatalf("resolved ask_user payload = %#v", results[0].Result)
	}
}
