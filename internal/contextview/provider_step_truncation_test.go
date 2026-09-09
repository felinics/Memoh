package contextview

import (
	"context"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

func loopSpanWithToolCycles(prefix []sdk.Message, cycles int, resultSize int) []sdk.Message {
	messages := append([]sdk.Message(nil), prefix...)
	for i := 0; i < cycles; i++ {
		callID := "call-" + string(rune('a'+i))
		messages = append(messages,
			assistantToolCallMessage(callID, "lookup", ""),
			toolResultMessage(callID, "lookup", strings.Repeat("x", resultSize)),
		)
	}
	return messages
}

func TestStepReselectionTruncatesOldToolResultsKeepsRecent(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	messages := loopSpanWithToolCycles(prefix, 3, 1000)

	selection := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:                 contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount:   len(prefix),
		Messages:              messages,
		KeepRecentToolResults: 1,
	})
	if selection.Messages == nil {
		t.Fatal("truncation must produce a message override")
	}
	if selection.Truncated != 2 {
		t.Fatalf("truncated = %d, want the two older results", selection.Truncated)
	}

	var toolResults []sdk.ToolResultPart
	for _, msg := range selection.Messages {
		if msg.Role != sdk.MessageRoleTool {
			continue
		}
		for _, part := range msg.Content {
			if result, ok := part.(sdk.ToolResultPart); ok {
				toolResults = append(toolResults, result)
			}
		}
	}
	if len(toolResults) != 3 {
		t.Fatalf("tool results = %d, want all three preserved as parts", len(toolResults))
	}
	for i, result := range toolResults[:2] {
		text, _ := result.Result.(string)
		if !strings.Contains(text, "pruned") {
			t.Fatalf("older result %d must be truncated, got %q", i, text)
		}
	}
	newest, _ := toolResults[2].Result.(string)
	if strings.Contains(newest, "pruned") {
		t.Fatalf("newest cycle must stay intact, got %q", newest)
	}
}

func TestStepReselectionTruncationPreservesExactLaterCarrierOrigin(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	messages := loopSpanWithToolCycles(prefix, 3, 1000)
	const marker = "<message sender=\"alice\">keep this injected carrier</message>"
	messages = append(messages, sdk.UserMessage(marker))

	selection := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:                 contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount:   len(prefix),
		Messages:              messages,
		KeepRecentToolResults: 1,
	})
	if selection.Messages == nil || selection.Truncated != 2 {
		t.Fatalf("selection = %+v, want two rewritten tool results", selection)
	}
	if !selection.MessageSourceIndexesKnown || len(selection.MessageSourceIndexes) != len(selection.Messages) {
		t.Fatalf("message origins = %#v/%t, want one exact origin per selected message", selection.MessageSourceIndexes, selection.MessageSourceIndexesKnown)
	}

	truncated := 0
	carrierIndex := -1
	for i, message := range selection.Messages {
		for _, part := range message.Content {
			if text, ok := part.(sdk.TextPart); ok && text.Text == marker {
				carrierIndex = i
			}
		}
		if message.Role != sdk.MessageRoleTool {
			continue
		}
		for _, part := range message.Content {
			result, ok := part.(sdk.ToolResultPart)
			if !ok {
				continue
			}
			text, _ := result.Result.(string)
			if strings.Contains(text, "pruned") {
				truncated++
				if selection.MessageSourceIndexes[i] != -1 {
					t.Fatalf("rewritten tool result origin = %d, want -1", selection.MessageSourceIndexes[i])
				}
			}
		}
	}
	if truncated != 2 {
		t.Fatalf("rewritten tool results = %d, want 2", truncated)
	}
	if carrierIndex < 0 {
		t.Fatal("selected messages lost the later injected carrier")
	}
	if got, want := selection.MessageSourceIndexes[carrierIndex], len(messages)-1; got != want {
		t.Fatalf("injected carrier origin = %d, want exact input index %d", got, want)
	}
}

func TestStepReselectionSkipsTruncationForSmallResults(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	messages := loopSpanWithToolCycles(prefix, 3, 40)

	selection := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:                 contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount:   len(prefix),
		Messages:              messages,
		KeepRecentToolResults: 1,
	})
	if selection.Truncated != 0 {
		t.Fatalf("small results must not be truncated, got %d", selection.Truncated)
	}
}

func TestStepReselectionTruncationRespectsMinMessages(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	messages := loopSpanWithToolCycles(prefix, 3, 1000)

	selection := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:                 contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount:   len(prefix),
		Messages:              messages,
		KeepRecentToolResults: 1,
		MinMessages:           50,
	})
	if selection.Truncated != 0 {
		t.Fatalf("below the message threshold nothing truncates, got %d", selection.Truncated)
	}
}

func TestStepReselectionWindowZeroAppliesThresholdHygieneDeterministically(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	messages := loopSpanWithToolCycles(prefix, 10, 1000)
	input := agentpkg.ContextStepSelectionInput{
		Scope:                 contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount:   len(prefix),
		Messages:              messages,
		BudgetMaxTokens:       0,
		KeepRecentToolResults: 4,
		MinMessages:           20,
	}

	first := SelectProviderStepMessages(context.Background(), input)
	second := SelectProviderStepMessages(context.Background(), input)
	if first.FatalError != nil || first.Dropped != 0 {
		t.Fatalf("window-zero selection enforced budget: %+v", first)
	}
	if first.Truncated != 6 {
		t.Fatalf("truncated = %d, want six old results with newest four intact", first.Truncated)
	}
	if first.Messages == nil ||
		first.Dropped != second.Dropped ||
		first.Truncated != second.Truncated ||
		!reflect.DeepEqual(first.DropReasons, second.DropReasons) ||
		!reflect.DeepEqual(first.Messages, second.Messages) {
		t.Fatalf("window-zero hygiene is not deterministic:\nfirst:  %#v\nsecond: %#v", first, second)
	}
}

func TestStepReselectionBudgetDropsKeepToolClosuresAtomic(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	messages := loopSpanWithToolCycles(prefix, 4, 800)

	selection := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:               contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount: len(prefix),
		Messages:            messages,
		// Leave room for the protected newest closure and trim notice while
		// forcing older closures out as atomic units.
		BudgetMaxTokens: 400,
	})
	if selection.Messages == nil || selection.Dropped == 0 {
		t.Fatalf("budget pressure must drop loop span content: %+v", selection)
	}

	calls := map[string]bool{}
	for _, msg := range selection.Messages {
		for _, part := range msg.Content {
			if call, ok := part.(sdk.ToolCallPart); ok {
				calls[call.ToolCallID] = true
			}
		}
	}
	for _, msg := range selection.Messages {
		for _, part := range msg.Content {
			if result, ok := part.(sdk.ToolResultPart); ok && !calls[result.ToolCallID] {
				t.Fatalf("orphan tool result survived budget drop: %s", result.ToolCallID)
			}
		}
	}
	for _, msg := range selection.Messages {
		if msg.Role != sdk.MessageRoleAssistant {
			continue
		}
		for _, part := range msg.Content {
			call, ok := part.(sdk.ToolCallPart)
			if !ok {
				continue
			}
			answered := false
			for _, other := range selection.Messages {
				for _, otherPart := range other.Content {
					if result, ok := otherPart.(sdk.ToolResultPart); ok && result.ToolCallID == call.ToolCallID {
						answered = true
					}
				}
			}
			if !answered {
				t.Fatalf("orphan tool call survived budget drop: %s", call.ToolCallID)
			}
		}
	}
	if reasons := selection.DropReasons; reasons["preserve_tool_closure"] > 0 {
		t.Fatalf("budget drops must report their droppable cause, not the closure tag: %#v", reasons)
	}
}
