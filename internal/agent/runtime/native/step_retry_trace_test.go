package native

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/felinics/twilight/provider/openai/completions"
	sdk "github.com/felinics/twilight/sdk"
)

// A failed attempt whose finish-step arrives after its error is drained
// before the retry; the retry's public step_end must carry its own usage.
func TestAgentStreamRetryStepEndCarriesTheRetriedRequestsUsage(t *testing.T) {
	t.Parallel()

	calls := 0
	provider := agentStreamTestProvider(func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
		calls++
		if calls == 1 {
			return closedAgentTestStream(
				&sdk.StartStepPart{},
				&sdk.TextDeltaPart{ID: "failed", Text: "failed answer"},
				&sdk.ErrorPart{Error: errors.New("connection reset by peer")},
				&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop, Usage: sdk.Usage{InputTokens: 11, OutputTokens: 1}},
			), nil
		}
		return closedAgentTestStream(
			&sdk.StartStepPart{},
			&sdk.TextDeltaPart{ID: "success", Text: "success answer"},
			&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop, Usage: sdk.Usage{InputTokens: 22, OutputTokens: 2}},
		), nil
	})
	var ends []StreamEvent
	for ev := range New(Deps{}).Stream(context.Background(), RunConfig{
		Model:    &sdk.Model{ID: "mock-model", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("task")},
		Identity: SessionContext{BotID: "bot-1"},
		Retry:    RetryConfig{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
	}) {
		if ev.Type == EventStepEnd {
			ends = append(ends, ev)
		}
	}
	if len(ends) != 1 {
		t.Fatalf("got %d step ends, want one successful retry", len(ends))
	}
	var usage sdk.Usage
	if err := json.Unmarshal(ends[0].Usage, &usage); err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 22 {
		t.Fatalf("the retried request's step_end carries the failed attempt's usage: got input %d, want 22", usage.InputTokens)
	}
}

type retryRoundTripper func(*http.Request) (*http.Response, error)

func (f retryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type retryResetReader struct{}

func (retryResetReader) Read([]byte) (int, error) { return 0, errors.New("connection reset by peer") }

// The same through Twilight's OpenAI completions provider over an in-memory
// transport, whose stream reports the finish-step before the read error.
func TestAgentStreamRetryStepEndWithOpenAIProviderCarriesTheRetriedRequestsUsage(t *testing.T) {
	t.Parallel()

	calls := 0
	client := &http.Client{Transport: retryRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		var body io.Reader
		if calls == 1 {
			body = io.MultiReader(strings.NewReader("data: {\"id\":\"failed\",\"model\":\"mock\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"failed answer\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":1,\"total_tokens\":12}}\n\n"), retryResetReader{})
		} else {
			body = strings.NewReader("data: {\"id\":\"success\",\"model\":\"mock\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"success answer\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":22,\"completion_tokens\":2,\"total_tokens\":24}}\n\ndata: [DONE]\n\n")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(body)}, nil
	})}
	provider := completions.New(completions.WithBaseURL("http://review.invalid"), completions.WithHTTPClient(client))
	var ends []StreamEvent
	for ev := range New(Deps{}).Stream(context.Background(), RunConfig{
		Model:    &sdk.Model{ID: "mock", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("task")},
		Identity: SessionContext{BotID: "bot-1"},
		Retry:    RetryConfig{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
	}) {
		if ev.Type == EventStepEnd {
			ends = append(ends, ev)
		}
	}
	if calls != 2 || len(ends) != 1 {
		t.Fatalf("got %d provider calls and %d step ends", calls, len(ends))
	}
	var usage sdk.Usage
	if err := json.Unmarshal(ends[0].Usage, &usage); err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 22 {
		t.Fatalf("the retried request's step_end carries the failed attempt's usage: got input %d, want 22", usage.InputTokens)
	}
}
