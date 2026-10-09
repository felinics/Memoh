package native

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/models/modelretry"
)

func generateRetryRun(provider sdk.Provider, retry modelretry.Config) RunConfig {
	return RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		Retry:            retry,
		RunID:            "run-generate-retry",
	}
}

// The non-streaming loop retries a failed provider call by the same rule as
// the streaming one: the same request is sent again, and the failed attempt
// is recorded once as a WARN event.
func TestAgentGenerateRetriesRetryableProviderFailure(t *testing.T) {
	t.Parallel()

	var requests []sdk.Request
	provider := &atomicMockProvider{
		handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
			requests = append(requests, cloneGenerateParams(params))
			if call == 1 {
				return sdk.ModelResult{}, serverErr()
			}
			return sdk.ModelResult{Text: "recovered", FinishReason: sdk.FinishReasonStop}, nil
		},
	}
	handler := &lifecycleRecordingHandler{}
	a := New(Deps{Logger: slog.New(handler)})

	result, err := a.Generate(context.Background(), generateRetryRun(provider, fastRetry))
	if err != nil {
		t.Fatalf("Generate() error = %v, want the retried call's answer", err)
	}
	if result.Text != "recovered" || provider.calls.Load() != 2 {
		t.Fatalf("Generate() = %q after %d calls, want recovered after 2", result.Text, provider.calls.Load())
	}
	if len(requests) != 2 || !providerAttemptContainsText(requests[1].Messages, "task") || len(requests[1].Messages) != len(requests[0].Messages) {
		t.Fatalf("retried request = %#v, want the failed request again", requests)
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()
	var recorded []slog.Record
	for _, record := range handler.records {
		if record.Message == "model call failed, retrying" {
			recorded = append(recorded, record)
		}
	}
	if len(recorded) != 1 || recorded[0].Level != slog.LevelWarn {
		t.Fatalf("retry records = %v, want one WARN event", recorded)
	}
	attrs := map[string]string{}
	recorded[0].Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value.String()
		return true
	})
	if attrs["run_id"] != "run-generate-retry" || attrs["attempt"] != "1" || attrs["retry_reason"] != string(modelretry.ReasonServerError) {
		t.Fatalf("retry record attrs = %v, want attempt 1 of the run for server_error", attrs)
	}
}

// A provider answer the policy does not retry ends the non-streaming run at
// once, still marked as a model call failure.
func TestAgentGenerateDoesNotRetryFinalProviderFailure(t *testing.T) {
	t.Parallel()

	provider := &atomicMockProvider{
		handler: func(int, sdk.Request) (sdk.ModelResult, error) {
			return sdk.ModelResult{}, rejectedErr()
		},
	}
	_, err := New(Deps{}).Generate(context.Background(), generateRetryRun(provider, fastRetry))
	var apiErr *sdk.APIError
	if !IsModelCallFailure(err) || !errors.As(err, &apiErr) || strings.Contains(err.Error(), "retries exhausted") {
		t.Fatalf("Generate() error = %v, want the rejected model call", err)
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 for a final failure", got)
	}
}

// The non-streaming loop gives up after the retry budget, wrapping the last
// failure as the streaming loop does.
func TestAgentGenerateGivesUpAfterRetryBudget(t *testing.T) {
	t.Parallel()

	provider := &atomicMockProvider{
		handler: func(int, sdk.Request) (sdk.ModelResult, error) {
			return sdk.ModelResult{}, rateLimitedErr()
		},
	}
	budget := modelretry.Config{MaxAttempts: 2, FastAttempts: 2}
	_, err := New(Deps{}).Generate(context.Background(), generateRetryRun(provider, budget))
	if !IsModelCallFailure(err) || !strings.Contains(err.Error(), "model call retries exhausted") {
		t.Fatalf("Generate() error = %v, want the exhausted retries", err)
	}
	if got := provider.calls.Load(); got != 3 {
		t.Fatalf("provider calls = %d, want the call and 2 retries", got)
	}
}

// scriptCutOff ends the stream without its finish-step part, as a dropped
// connection does when the provider sends no terminal event.
func scriptCutOff(partialText string) func(chan<- sdk.StreamPart) {
	return func(ch chan<- sdk.StreamPart) {
		ch <- &sdk.StartPart{}
		ch <- &sdk.StartStepPart{}
		ch <- &sdk.TextStartPart{ID: "mock"}
		ch <- &sdk.TextDeltaPart{ID: "mock", Text: partialText}
	}
}

func streamRetryEvents(t *testing.T, provider sdk.Provider) []StreamEvent {
	t.Helper()
	var events []StreamEvent
	for ev := range New(Deps{}).Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		Retry:            fastRetry,
	}) {
		events = append(events, ev)
	}
	if terminal := events[len(events)-1]; terminal.Type != EventAgentEnd {
		t.Fatalf("terminal event = %q, want %q after the retry", terminal.Type, EventAgentEnd)
	}
	var retries []StreamEvent
	for _, ev := range events {
		if ev.Type == EventRetry {
			retries = append(retries, ev)
		}
	}
	return retries
}

// A stream that closes before its finish-step part was cut off; it is retried
// like the SDK's ErrStreamIncomplete.
func TestAgentStreamRetriesStreamEndedBeforeFinishStep(t *testing.T) {
	t.Parallel()

	var invocations atomic.Int32
	provider := &atomicMockProvider{}
	provider.stream = streamScript(&invocations, scriptCutOff("half an ans"), scriptText("whole answer"))

	retries := streamRetryEvents(t, provider)
	if got := invocations.Load(); got != 2 {
		t.Fatalf("provider invocations = %d, want the cut-off call and its retry", got)
	}
	if len(retries) != 1 || retries[0].RetryReason != string(modelretry.ReasonStreamIncomplete) || retries[0].RetryDelayMs != 0 {
		t.Fatalf("retry events = %#v, want one immediate stream_incomplete retry", retries)
	}
}

// The retry event carries the wait the provider asked for and the failure's
// class, on the wire as well; the provider's text stays off it.
func TestAgentStreamRetryEventCarriesRetryAfterAndReason(t *testing.T) {
	t.Parallel()

	header := http.Header{}
	header.Set("retry-after-ms", "20")
	limited := &sdk.APIError{Provider: "mock", StatusCode: 429, Kind: sdk.KindRateLimited, Message: "slow down", Header: header}
	var invocations atomic.Int32
	provider := &atomicMockProvider{}
	provider.stream = streamScript(&invocations, scriptStreamError("", limited), scriptText("recovered"))

	retries := streamRetryEvents(t, provider)
	if len(retries) != 1 {
		t.Fatalf("retry events = %#v, want one", retries)
	}
	retry := retries[0]
	if retry.Attempt != 1 || retry.MaxAttempt != fastRetry.MaxAttempts || retry.RetryDelayMs != 20 ||
		retry.RetryReason != string(modelretry.ReasonRateLimited) || retry.Error != "" || retry.Cause != nil {
		t.Fatalf("retry event = %#v, want attempt 1 after the provider's 20ms for rate_limited", retry)
	}
	wire, err := json.Marshal(retry)
	if err != nil {
		t.Fatalf("marshal retry event: %v", err)
	}
	if want := `{"type":"retry","attempt":1,"maxAttempt":5,"retryDelayMs":20,"retryReason":"rate_limited"}`; string(wire) != want {
		t.Fatalf("retry frame = %s, want %s", wire, want)
	}
}
