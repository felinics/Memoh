package contextview

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	agentpkg "github.com/memohai/memoh/internal/agent/runtime/native"
)

func TestProviderStepReselectionFailsClosedForSerializedS3HugeResult(t *testing.T) {
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
	_, fixedBytes := contextfrag.ProviderPayloadHashAndBytes(system, prefix, tools)
	_, candidateBytes := contextfrag.ProviderPayloadHashAndBytes(system, messages, tools)
	if candidateTokens := contextfrag.ProviderBudgetTokensFromBytes(candidateBytes); candidateTokens <= inputAllowance {
		t.Fatalf("literal S3 candidate = %d tokens, want over allowance %d", candidateTokens, inputAllowance)
	}
	suffixBudget := inputAllowance - contextfrag.ProviderBudgetTokensFromBytes(fixedBytes)

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
		t.Fatalf("fatal serialized overflow returned provider messages: %#v", result.Messages)
	}
}

func TestProviderStepReselectionTightensSerializedS3HugeResultWhenDroppable(t *testing.T) {
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
	_, fixedBytes := contextfrag.ProviderPayloadHashAndBytes(system, prefix, tools)
	_, candidateBytes := contextfrag.ProviderPayloadHashAndBytes(system, messages, tools)
	if candidateTokens := contextfrag.ProviderBudgetTokensFromBytes(candidateBytes); candidateTokens <= inputAllowance {
		t.Fatalf("literal S3 candidate = %d tokens, want over allowance %d", candidateTokens, inputAllowance)
	}

	result := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:                        contextfrag.Scope{BotID: "contextbench"},
		InitialMessageCount:          len(prefix),
		Messages:                     messages,
		BudgetMaxTokens:              inputAllowance - contextfrag.ProviderBudgetTokensFromBytes(fixedBytes),
		ProviderSystem:               system,
		ProviderTools:                tools,
		ProviderInputAllowanceTokens: inputAllowance,
	})
	if result.FatalError != nil {
		t.Fatalf("step reselection failed instead of tightening droppable history: %v", result.FatalError)
	}
	if result.Messages == nil || result.Dropped == 0 {
		t.Fatalf("serialized overflow was not tightened: %+v", result)
	}
	if !reflect.DeepEqual(result.Messages[:len(prefix)], prefix) {
		t.Fatalf("immutable prefix changed: %#v", result.Messages[:len(prefix)])
	}
	_, selectedBytes := contextfrag.ProviderPayloadHashAndBytes(system, result.Messages, tools)
	if got := contextfrag.ProviderBudgetTokensFromBytes(selectedBytes); got > inputAllowance {
		t.Fatalf("nonfatal provider payload = %d tokens, want <= allowance %d", got, inputAllowance)
	}
}

func TestProviderStepReselectionAllowsExactlyFittingProtectedSerializedSuffix(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage(strings.Repeat("prefix", 137))}
	loop := []sdk.Message{
		assistantToolCallMessage("exact-fit", "exec", ""),
		toolResultMessage("exact-fit", "exec", strings.Repeat("result", 211)),
	}
	messages := append(append([]sdk.Message(nil), prefix...), loop...)
	system := strings.Repeat("system", 83)
	tools := []sdk.Tool{{Name: "exec", Description: "Execute a bounded command."}}
	_, fixedBytes := contextfrag.ProviderPayloadHashAndBytes(system, prefix, tools)
	_, candidateBytes := contextfrag.ProviderPayloadHashAndBytes(system, messages, tools)
	fixedTokens := contextfrag.ProviderBudgetTokensFromBytes(fixedBytes)
	allowance := contextfrag.ProviderBudgetTokensFromBytes(candidateBytes)
	suffixBudget := allowance - fixedTokens

	singletonCost := 0
	for _, message := range loop {
		_, messageBytes := contextfrag.ProviderPayloadHashAndBytes("", []sdk.Message{message}, nil)
		singletonCost += contextfrag.ProviderBudgetTokensFromBytes(messageBytes)
	}
	if singletonCost <= suffixBudget {
		t.Fatalf("fixture singleton cost = %d, want greater than exact additive suffix %d", singletonCost, suffixBudget)
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
}
