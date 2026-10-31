package grok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	"github.com/felinics/memoh/internal/errs"
)

func TestMCPToolCallReadsTheQualifiedName(t *testing.T) {
	// The raw input of a use_tool permission request from Grok 1.0.40.
	server, tool, ok := mcpToolCall(map[string]any{"variant": "UseTool", "tool_name": "Memoh_Tools__send_message", "tool_input": map[string]any{"text": "hi"}})
	if !ok || server != "Memoh_Tools" || tool != "send_message" {
		t.Fatalf("mcpToolCall() = %q, %q, %v", server, tool, ok)
	}
	for _, input := range []any{nil, map[string]any{"command": "ls"}, map[string]any{"tool_name": "send_message"}, map[string]any{"tool_name": "__x"}} {
		if _, _, ok := mcpToolCall(input); ok {
			t.Errorf("mcpToolCall(%v) recognized an MCP call", input)
		}
	}
}

func TestUsageFromMeta(t *testing.T) {
	// The shape Grok 1.0.40 returns in a prompt response.
	usage := usageFromMeta(map[string]any{"usage": map[string]any{
		"inputTokens": float64(142456), "outputTokens": float64(387), "totalTokens": float64(142843),
		"cachedReadTokens": float64(98176), "cacheCreationTokens": float64(0), "reasoningTokens": float64(228),
	}})
	if usage == nil || usage.InputTokens != 142456 || usage.OutputTokens != 387 || usage.TotalTokens != 142843 ||
		usage.CachedInputTokens != 98176 || usage.InputTokenDetails.NoCacheTokens != 44280 || usage.ReasoningTokens != 228 {
		t.Fatalf("usageFromMeta() = %+v", usage)
	}
	if usageFromMeta(map[string]any{"totalTokens": float64(1)}) != nil {
		t.Fatal("a response without _meta.usage must report no usage")
	}
}

func TestTurnFailureReportsWhatGrokSaid(t *testing.T) {
	apiError := func(status int, message string) error {
		// The layers a JSON-RPC error crosses on its way out of the client.
		return fmt.Errorf("send ACP prompt: %w", errs.WrapDependency(&acp.RequestError{
			Code: -32603, Message: "Internal error",
			Data: map[string]any{"http_status": float64(status), "message": message},
		}, ""))
	}
	cases := []struct {
		name    string
		err     error
		kind    external.FailureKind
		message string
	}{
		{"no credential", &acp.RequestError{Code: authRequiredCode, Message: "Authentication required"}, external.FailureAuthRequired, "Authentication required"},
		{"unauthorized", apiError(401, "unauthorized"), external.FailureAuthRequired, "unauthorized"},
		{"throttled", apiError(429, "slow down"), external.FailureReported, "slow down"},
		{"rejected key", apiError(400, "Incorrect API key provided."), external.FailureReported, "Incorrect API key provided."},
	}
	for _, tc := range cases {
		var failure *external.Failure
		if got := turnFailure(tc.err); !errors.As(got, &failure) || failure.Kind != tc.kind || failure.Message != tc.message {
			t.Errorf("%s: turnFailure() = %+v, want kind %d with %q", tc.name, failure, tc.kind, tc.message)
		}
	}
	// An error Grok did not answer is reported in the words it failed with.
	plain := errors.New("connection reset")
	var failure *external.Failure
	if got := turnFailure(plain); !errors.As(got, &failure) || failure.Kind != external.FailureReported || failure.Message != "connection reset" {
		t.Errorf("turnFailure(%v) = %v, want it reported verbatim", plain, got)
	}
}

func TestOnlyAMissingSessionFileMeansGrokLostTheSession(t *testing.T) {
	ctx := context.Background()
	answer := func(code int, data any) error {
		return fmt.Errorf("resume ACP session: %w", errs.WrapDependency(&acp.RequestError{Code: code, Message: "Internal error", Data: data}, ""))
	}
	// What Grok 1.0.40 answers for a session id it has no file for.
	gone := answer(-32603, map[string]any{"code": "FS_NOT_FOUND"})
	if !sessionGone(gone) {
		t.Error("a session Grok has no file for must count as gone")
	}
	if err := resumeFailure(ctx, fmt.Errorf("%w: %w", acpclient.ErrNativeSessionGone, gone)); !errors.Is(err, external.ErrThreadUnavailable) {
		t.Errorf("a control on a lost session = %v, want ErrThreadUnavailable", err)
	}
	// The same error code without that reason is how a broken connection and
	// any other failure arrive: the session may well still be there.
	for name, tc := range map[string]struct {
		err  error
		want external.FailureKind
	}{
		"another internal error": {answer(-32603, map[string]any{"error": "EOF"}), external.FailureReported},
		"no credential":          {answer(authRequiredCode, nil), external.FailureAuthRequired},
	} {
		if sessionGone(tc.err) {
			t.Errorf("%s: counted as a lost session", name)
		}
		var failure *external.Failure
		if err := resumeFailure(ctx, tc.err); !errors.As(err, &failure) || failure.Kind != tc.want {
			t.Errorf("%s: resumeFailure() = %v, want kind %d and the stored session kept", name, err, tc.want)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := resumeFailure(cancelled, gone); !errors.Is(err, context.Canceled) {
		t.Errorf("a stopped turn = %v, want its cancellation", err)
	}
}

func TestCatalogFromStateReadsEachModelsEfforts(t *testing.T) {
	// Trimmed from the initialize response of Grok 1.0.52.
	var meta map[string]any
	if err := json.Unmarshal([]byte(`{"modelState": {"currentModelId": "grok-4.7", "availableModels": [
		{"modelId": "grok-4.7", "name": "Grok 4.7", "description": "latest", "_meta": {"supportsReasoningEffort": true, "reasoningEffort": "high",
			"reasoningEfforts": [{"id": "xhigh", "value": "xhigh", "label": "Extra High"}, {"id": "high", "value": "high", "label": "High", "default": true}]}},
		{"modelId": "grok-4.5", "name": "Grok 4.5", "_meta": {"supportsReasoningEffort": true, "reasoningEffort": "high",
			"reasoningEfforts": [{"id": "high", "value": "high", "label": "High", "default": true}, {"id": "low", "value": "low", "label": "Low"}]}}]}}`), &meta); err != nil {
		t.Fatal(err)
	}
	state, _ := meta["modelState"].(map[string]any)
	models, err := catalogFromState(state)
	if err != nil || len(models) != 2 {
		t.Fatalf("catalog = %+v, %v", models, err)
	}
	if !models[0].Default || models[1].Default || models[1].DefaultReasoningEffort != "high" ||
		len(models[1].ReasoningEfforts) != 2 || models[1].ReasoningEfforts[1].ID != "low" {
		t.Fatalf("catalog = %+v, want each model with its own efforts", models)
	}
	if _, err := catalogFromState(nil); err == nil {
		t.Fatal("a Grok that reports no catalog must be an error, not an empty catalog")
	}
}

func TestUserQuestionsSpeakGrokAskUserQuestion(t *testing.T) {
	// The request Grok 1.0.52 sends for its ask_user_question tool.
	toolCallID, questions, err := userQuestions.Decode(json.RawMessage(`{"sessionId": "s1", "toolCallId": "call-1", "mode": "default",
		"questions": [{"question": "A or B?", "options": [{"label": "A", "description": "first"}, {"label": "B", "description": "second"}], "multiSelect": null}]}`))
	if err != nil || toolCallID != "call-1" || len(questions) != 1 || questions[0].MultiSelect || len(questions[0].Options) != 2 || questions[0].Options[1].Description != "second" {
		t.Fatalf("decoded %q %+v, %v", toolCallID, questions, err)
	}
	// The response shapes Grok accepts, checked against its parser.
	answered, _ := json.Marshal(userQuestions.Encode(questions, []string{"B"}))
	declined, _ := json.Marshal(userQuestions.Encode(questions, nil))
	if string(answered) != `{"answers":{"A or B?":"B"},"outcome":"accepted"}` || string(declined) != `{"outcome":"cancelled"}` {
		t.Fatalf("answered %s, declined %s", answered, declined)
	}
}
