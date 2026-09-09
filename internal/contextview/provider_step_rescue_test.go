package contextview

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

func collectToolResults(messages []sdk.Message) []sdk.ToolResultPart {
	var results []sdk.ToolResultPart
	for _, msg := range messages {
		if msg.Role != sdk.MessageRoleTool {
			continue
		}
		for _, part := range msg.Content {
			if result, ok := part.(sdk.ToolResultPart); ok {
				results = append(results, result)
			}
		}
	}
	return results
}

func TestStepReselectionRescuesProtectedOverflowByPruningInFlightResults(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	messages := loopSpanWithToolCycles(prefix, 1, 8_000)

	selection := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:                 contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount:   len(prefix),
		Messages:              messages,
		BudgetMaxTokens:       300,
		KeepRecentToolResults: 4,
		MinMessages:           20,
	})

	if selection.FatalError != nil {
		t.Fatalf("FatalError = %v, want in-flight results pruned instead of a failed run", selection.FatalError)
	}
	if selection.Messages == nil {
		t.Fatal("rescue must produce a message override")
	}
	if selection.ProtectedPruned < 1 || selection.Truncated < 1 {
		t.Fatalf("pruned = %d truncated = %d, want the protected result stubbed", selection.ProtectedPruned, selection.Truncated)
	}
	results := collectToolResults(selection.Messages)
	if len(results) != 1 {
		t.Fatalf("tool results = %d, want the exchange preserved as parts", len(results))
	}
	text, _ := results[0].Result.(string)
	if !strings.Contains(text, "pruned") {
		t.Fatalf("protected result must carry the pruned stub, got %q", text)
	}
}

func TestStepReselectionRescueEscalatesToNewestResultAsLastResort(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	messages := loopSpanWithToolCycles(prefix, 5, 4_000)

	selection := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:                 contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount:   len(prefix),
		Messages:              messages,
		BudgetMaxTokens:       600,
		KeepRecentToolResults: 4,
		MinMessages:           20,
	})

	if selection.FatalError != nil {
		t.Fatalf("FatalError = %v, want escalating prune instead of a failed run", selection.FatalError)
	}
	results := collectToolResults(selection.Messages)
	if len(results) == 0 {
		t.Fatal("expected surviving tool results")
	}
	newest, _ := results[len(results)-1].Result.(string)
	if !strings.Contains(newest, "pruned") {
		t.Fatalf("newest result must be stubbed once older prunes cannot fit the budget, got %q", newest)
	}
}

func TestStepReselectionRescueKeepsNewestResultWhenOlderPruneSuffices(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	newestBody := strings.Repeat("keep", 200)
	messages := append(append([]sdk.Message(nil), prefix...),
		sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			sdk.ToolCallPart{ToolCallID: "call-old", ToolName: "lookup", Input: map[string]any{}},
			sdk.ToolCallPart{ToolCallID: "call-new", ToolName: "lookup", Input: map[string]any{}},
		}},
		toolResultMessage("call-old", "lookup", strings.Repeat("x", 8_000)),
		toolResultMessage("call-new", "lookup", newestBody),
	)

	selection := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:                 contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount:   len(prefix),
		Messages:              messages,
		BudgetMaxTokens:       400,
		KeepRecentToolResults: 4,
		MinMessages:           20,
	})

	if selection.FatalError != nil {
		t.Fatalf("FatalError = %v, want the older protected result pruned instead", selection.FatalError)
	}
	if selection.ProtectedPruned != 1 {
		t.Fatalf("ProtectedPruned = %d, want exactly the older result stubbed", selection.ProtectedPruned)
	}
	results := collectToolResults(selection.Messages)
	if len(results) != 2 {
		t.Fatalf("tool results = %d, want both preserved as parts", len(results))
	}
	older, _ := results[0].Result.(string)
	newest, _ := results[1].Result.(string)
	if !strings.Contains(older, "pruned") {
		t.Fatalf("older result must be stubbed, got %q", older)
	}
	if newest != newestBody {
		t.Fatalf("newest result must survive intact when pruning older ones suffices, got %q", newest)
	}
}

func TestStepReselectionRescuePreservesSiblingsInParallelBatch(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	messages := append(append([]sdk.Message(nil), prefix...),
		sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			sdk.ToolCallPart{ToolCallID: "call-big", ToolName: "exec", Input: map[string]any{}},
			sdk.ToolCallPart{ToolCallID: "call-small", ToolName: "exec", Input: map[string]any{}},
		}},
		sdk.Message{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{
			sdk.ToolResultPart{ToolCallID: "call-big", ToolName: "exec", Result: strings.Repeat("x", 8_000), IsError: true},
			sdk.ToolResultPart{ToolCallID: "call-small", ToolName: "exec", Result: "ok: created id-42"},
		}},
	)

	selection := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:                 contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount:   len(prefix),
		Messages:              messages,
		BudgetMaxTokens:       400,
		KeepRecentToolResults: 4,
		MinMessages:           20,
	})

	if selection.FatalError != nil {
		t.Fatalf("FatalError = %v, want the batched exchange pruned instead", selection.FatalError)
	}
	results := collectToolResults(selection.Messages)
	if len(results) != 2 {
		t.Fatalf("tool results = %d, want both parts preserved", len(results))
	}
	big, small := results[0], results[1]
	if text, _ := big.Result.(string); !strings.Contains(text, "pruned") {
		t.Fatalf("oversized part must be stubbed, got %v", big.Result)
	}
	if !big.IsError {
		t.Fatal("a failed result must not read as success after the rescue")
	}
	if small.Result != "ok: created id-42" {
		t.Fatalf("small sibling must survive verbatim, got %v", small.Result)
	}
}

func TestStepReselectionFailsClosedWhenEvenStubbedExchangeOverflows(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	messages := loopSpanWithToolCycles(prefix, 1, 8_000)

	selection := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:                 contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount:   len(prefix),
		Messages:              messages,
		BudgetMaxTokens:       1,
		KeepRecentToolResults: 4,
		MinMessages:           20,
	})

	if !errors.Is(selection.FatalError, contextfrag.ErrProtectedContextOverflow) {
		t.Fatalf("FatalError = %v, want fail-closed when the stubbed exchange still overflows", selection.FatalError)
	}
}

func TestStepReselectionProtectedOverflowFailsClosedWhenNothingPrunable(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("task")}
	messages := append(append([]sdk.Message(nil), prefix...),
		sdk.UserMessage(strings.Repeat("injected carrier ", 500)),
	)

	selection := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:                 contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount:   len(prefix),
		Messages:              messages,
		BudgetMaxTokens:       300,
		KeepRecentToolResults: 4,
		MinMessages:           20,
	})

	if !errors.Is(selection.FatalError, contextfrag.ErrProtectedContextOverflow) {
		t.Fatalf("FatalError = %v, want protected overflow to stay fail-closed", selection.FatalError)
	}
}
