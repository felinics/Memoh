package modelretry

import (
	"context"
	"errors"
	"fmt"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func TestContextOverflowRequiresProviderTokenRejection(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"http code", &sdk.APIError{StatusCode: 400, Code: "context_length_exceeded"}, true},
		{"stream code", fmt.Errorf("stream: %w", &sdk.APIError{Code: "context_length_exceeded"}), true},
		{"anthropic", &sdk.APIError{StatusCode: 400, Type: "invalid_request_error", Message: "prompt is too long: 218601 tokens > 200000 maximum"}, true},
		{"google", &sdk.APIError{StatusCode: 400, Message: "The input token count (218601) exceeds the maximum number of tokens allowed (200000)."}, true},
		{"openai compatible", &sdk.APIError{StatusCode: 400, Message: "This model's maximum context length is 200000 tokens. However, you requested 218601 tokens."}, true},
		{"ordinary parameter", &sdk.APIError{StatusCode: 400, Message: "invalid max_tokens"}, false},
		{"body cap", &sdk.APIError{StatusCode: 413, Code: "LLM_GATEWAY_REQUEST_TOO_LARGE"}, false},
		{"untyped body cap", &sdk.APIError{StatusCode: 413, Message: "request too large"}, false},
		{"output cap", &sdk.APIError{StatusCode: 400, Message: "max_tokens exceeds model output limit"}, false},
		{"rate limit", &sdk.APIError{StatusCode: 429, Kind: sdk.KindRateLimited, Message: "token limit exceeded"}, false},
		{"ordinary error", errors.New("context_length_exceeded"), false},
		{"cancelled", errors.Join(context.Canceled, &sdk.APIError{Code: "context_length_exceeded"}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, got := ContextOverflow(tc.err)
			if got != tc.want {
				t.Fatalf("ContextOverflow() = %v, want %v", got, tc.want)
			}
			if tc.want {
				if _, retry := Retryable(tc.err); retry {
					t.Fatal("overflow must not retry the unchanged payload")
				}
			}
		})
	}
}

func TestContextOverflowFeedbackBoundsSmallerInput(t *testing.T) {
	feedback, ok := ContextOverflow(&sdk.APIError{Type: "invalid_request_error", Message: "prompt is too long: 218601 tokens > 200000 maximum"})
	if !ok || feedback.InputTokens != 218601 || feedback.LimitTokens != 200000 {
		t.Fatalf("feedback=%+v ok=%v", feedback, ok)
	}
	// Without counts the request still shrinks. With them it shrinks by how
	// far the provider found it over, less a margin: more than the fixed cut
	// when far over, less when just over.
	if got := (Overflow{}).InputBudget(180000); got != 135000 {
		t.Fatalf("unknown rejection budget=%d", got)
	}
	if got := (Overflow{InputTokens: 300000, LimitTokens: 200000}).InputBudget(180000); got != 114000 {
		t.Fatalf("measured rejection budget=%d", got)
	}
	if got := feedback.InputBudget(180000); got != 156449 {
		t.Fatalf("slightly over rejection budget=%d", got)
	}
}

// Counts are the input's. The OpenAI-compatible form shares its window with
// the completion: a split request leaves the input the window less the
// completion, and a bare requested total, which mixes the two, is no count.
func TestContextOverflowReadsInputCounts(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    Overflow
	}{
		{"prompt is too long: 218601 tokens > 200000 maximum", Overflow{InputTokens: 218601, LimitTokens: 200000}},
		{"The input token count (218601) exceeds the maximum number of tokens allowed (200000).", Overflow{InputTokens: 218601, LimitTokens: 200000}},
		{"This model's maximum context length is 32768 tokens. However, your messages resulted in 33000 tokens. Please reduce the length of the messages.", Overflow{InputTokens: 33000, LimitTokens: 32768}},
		{"This model's maximum context length is 32768 tokens. However, you requested 33792 tokens (25600 in the messages, 8192 in the completion). Please reduce the length of the messages or completion.", Overflow{InputTokens: 25600, LimitTokens: 24576}},
		{"This model's maximum context length is 200000 tokens. However, you requested 218601 tokens.", Overflow{}},
	} {
		got, ok := ContextOverflow(&sdk.APIError{StatusCode: 400, Type: "invalid_request_error", Message: tc.message})
		if !ok || got != tc.want {
			t.Fatalf("%q: feedback=%+v ok=%v, want %+v", tc.message, got, ok, tc.want)
		}
	}
}
