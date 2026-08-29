package contextview

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

func TestProviderStepReselectionFailsClosedForEnvelopeS3HugeResult(t *testing.T) {
	t.Parallel()
	const (
		inputAllowance = 24_000
		resultBytes    = 65_871
	)

	prefix := []sdk.Message{sdk.UserMessage(strings.Repeat("p", 3_000))}
	messages := append([]sdk.Message(nil), prefix...)
	resultText := strings.Repeat("r", resultBytes)
	if len(resultText) != resultBytes {
		t.Fatalf("raw S3 result bytes = %d, want %d", len(resultText), resultBytes)
	}
	messages = append(messages,
		assistantToolCallMessage("contextbench-s3-003", "exec", ""),
		toolResultMessage("contextbench-s3-003", "exec", resultText),
	)
	system := strings.Repeat("s", 10_000)
	tools := []sdk.Tool{{
		Name: "exec", Description: "Execute a bounded command.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}},
	}}
	if candidateTokens := contextfrag.ProviderEnvelopeTokens(system, messages, tools); candidateTokens <= inputAllowance {
		t.Fatalf("literal S3 candidate = %d tokens, want over allowance %d", candidateTokens, inputAllowance)
	}
	suffixBudget := inputAllowance - contextfrag.ProviderEnvelopeTokens(system, prefix, tools)

	result := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:                        contextfrag.Scope{BotID: "contextbench"},
		InitialMessageCount:          len(prefix),
		Messages:                     messages,
		BudgetMaxTokens:              suffixBudget,
		ProviderSystem:               system,
		ProviderTools:                tools,
		ProviderInputAllowanceTokens: inputAllowance,
	})
	if !errors.Is(result.FatalError, contextfrag.ErrProtectedContextOverflow) {
		t.Fatalf("step reselection error = %v, want protected overflow for the newest tool closure", result.FatalError)
	}
	if result.Messages != nil {
		t.Fatalf("fatal envelope overflow returned provider messages: %#v", result.Messages)
	}
}

func TestProviderStepReselectionTightensEnvelopeS3HugeResultWhenDroppable(t *testing.T) {
	t.Parallel()
	const (
		inputAllowance = 24_000
		resultBytes    = 65_871
	)

	prefix := []sdk.Message{sdk.UserMessage(strings.Repeat("p", 3_000))}
	messages := append([]sdk.Message(nil), prefix...)
	messages = append(messages,
		assistantToolCallMessage("contextbench-s3-003", "exec", ""),
		toolResultMessage("contextbench-s3-003", "exec", strings.Repeat("r", resultBytes)),
		assistantToolCallMessage("contextbench-s3-004", "exec", ""),
		toolResultMessage("contextbench-s3-004", "exec", "small protected result"),
	)
	system := strings.Repeat("s", 10_000)
	tools := []sdk.Tool{{
		Name: "exec", Description: "Execute a bounded command.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}},
	}}
	if candidateTokens := contextfrag.ProviderEnvelopeTokens(system, messages, tools); candidateTokens <= inputAllowance {
		t.Fatalf("literal S3 candidate = %d tokens, want over allowance %d", candidateTokens, inputAllowance)
	}

	result := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:                        contextfrag.Scope{BotID: "contextbench"},
		InitialMessageCount:          len(prefix),
		Messages:                     messages,
		BudgetMaxTokens:              inputAllowance - contextfrag.ProviderEnvelopeTokens(system, prefix, tools),
		ProviderSystem:               system,
		ProviderTools:                tools,
		ProviderInputAllowanceTokens: inputAllowance,
	})
	if result.FatalError != nil {
		t.Fatalf("step reselection failed instead of tightening droppable history: %v", result.FatalError)
	}
	if result.Messages == nil || result.Dropped == 0 {
		t.Fatalf("envelope overflow was not tightened: %+v", result)
	}
	if !reflect.DeepEqual(result.Messages[:len(prefix)], prefix) {
		t.Fatalf("immutable prefix changed: %#v", result.Messages[:len(prefix)])
	}
	if got := contextfrag.ProviderEnvelopeTokens(system, result.Messages, tools); got > inputAllowance {
		t.Fatalf("nonfatal provider payload = %d tokens, want <= allowance %d", got, inputAllowance)
	}
}

func TestProviderStepReselectionAllowsExactlyFittingProtectedEnvelopeSuffix(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage(strings.Repeat("prefix", 137))}
	loop := []sdk.Message{
		assistantToolCallMessage("exact-fit", "exec", ""),
		toolResultMessage("exact-fit", "exec", strings.Repeat("result", 211)),
	}
	messages := append(append([]sdk.Message(nil), prefix...), loop...)
	system := strings.Repeat("system", 83)
	tools := []sdk.Tool{{Name: "exec", Description: "Execute a bounded command."}}
	allowance := contextfrag.ProviderEnvelopeTokens(system, messages, tools)
	suffixBudget := allowance - contextfrag.ProviderEnvelopeTokens(system, prefix, tools)
	if selectionCost := contextfrag.ProviderEnvelopeTokens("", loop, nil); selectionCost != suffixBudget {
		t.Fatalf("fixture suffix cost = %d, want exact additive suffix budget %d", selectionCost, suffixBudget)
	}

	result := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:                        contextfrag.Scope{BotID: "contextbench"},
		InitialMessageCount:          len(prefix),
		Messages:                     messages,
		BudgetMaxTokens:              suffixBudget,
		ProviderSystem:               system,
		ProviderTools:                tools,
		ProviderInputAllowanceTokens: allowance,
	})
	if result.FatalError != nil {
		t.Fatalf("exact-fit protected suffix failed admission: %v", result.FatalError)
	}
	if result.Messages != nil || result.Dropped != 0 || result.Truncated != 0 {
		t.Fatalf("exact-fit protected suffix changed: %+v", result)
	}
	if result := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:                        contextfrag.Scope{BotID: "contextbench"},
		InitialMessageCount:          len(prefix),
		Messages:                     messages,
		BudgetMaxTokens:              suffixBudget - 1,
		ProviderSystem:               system,
		ProviderTools:                tools,
		ProviderInputAllowanceTokens: allowance - 1,
	}); !errors.Is(result.FatalError, contextfrag.ErrProtectedContextOverflow) {
		t.Fatalf("one token short of the protected suffix must fail closed, got %v", result.FatalError)
	}
}
