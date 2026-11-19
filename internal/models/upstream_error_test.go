package models

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"
)

func TestUpstreamErrorSummary(t *testing.T) {
	t.Parallel()

	t.Run("nil error returns empty", func(t *testing.T) {
		t.Parallel()
		if got := UpstreamErrorSummary(nil); got != "" {
			t.Fatalf("UpstreamErrorSummary(nil) = %q, want empty", got)
		}
	})

	t.Run("transport and internal errors pass through verbatim in OSS", func(t *testing.T) {
		t.Parallel()
		plain := errors.New("db connection reset")
		if got := UpstreamErrorSummary(plain); got != "db connection reset" {
			t.Fatalf("UpstreamErrorSummary(%v) = %q, want %q", plain, got, "db connection reset")
		}
		urlErr := &url.Error{Op: "Post", URL: "https://api.example.com/v1", Err: errors.New("connection refused")}
		if got, want := UpstreamErrorSummary(urlErr), urlErr.Error(); got != want {
			t.Fatalf("UpstreamErrorSummary(%v) = %q, want %q", urlErr, got, want)
		}
	})

	t.Run("401 and KindAuthentication pass through verbatim without redaction", func(t *testing.T) {
		t.Parallel()
		err := &sdk.APIError{
			StatusCode: http.StatusUnauthorized,
			Kind:       sdk.KindAuthentication,
			Code:       "invalid_api_key",
			Message:    "Incorrect API key provided: sk-live-ab12****yz89",
			RequestID:  "req_auth",
		}
		want := "HTTP 401 · invalid_api_key: Incorrect API key provided: sk-live-ab12****yz89\nProvider Request ID: req_auth"
		if got := UpstreamErrorSummary(err); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("synthetic error without diagnostic fields returns empty", func(t *testing.T) {
		t.Parallel()
		err := &sdk.APIError{StatusCode: http.StatusBadRequest, Kind: sdk.KindRateLimited}
		if got := UpstreamErrorSummary(err); got != "" {
			t.Fatalf("UpstreamErrorSummary(%v) = %q, want empty", err, got)
		}
	})

	t.Run("structured summary omits Body when Message is set", func(t *testing.T) {
		t.Parallel()
		err := fmt.Errorf("wrapped: %w", &sdk.APIError{
			StatusCode: http.StatusBadRequest,
			Type:       "invalid_request_error",
			Code:       "model_not_found",
			Message:    "The model does not exist",
			RequestID:  "req_123",
			Body:       []byte(`{"input":"ECHOED_USER_INPUT","error":{"message":"The model does not exist"}}`),
		})
		want := "HTTP 400 · model_not_found: The model does not exist\nProvider Request ID: req_123"
		if got := UpstreamErrorSummary(err); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("falls back to valid UTF-8 Body only when Message is empty", func(t *testing.T) {
		t.Parallel()
		err := &sdk.APIError{
			StatusCode: http.StatusBadGateway,
			Body:       []byte("<html>502 Bad Gateway</html>"),
		}
		if got, want := UpstreamErrorSummary(err), "HTTP 502: <html>502 Bad Gateway</html>"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}

		binaryErr := &sdk.APIError{
			StatusCode: http.StatusBadGateway,
			Type:       "server_error",
			Body:       []byte{0xff, 0xfe, 0xfd},
		}
		if got, want := UpstreamErrorSummary(binaryErr), "HTTP 502 · server_error"; got != want {
			t.Fatalf("binary fallback got %q, want %q", got, want)
		}

		emptyJSON := &sdk.APIError{
			StatusCode: http.StatusBadRequest,
			Body:       []byte(" {} "),
		}
		if got := UpstreamErrorSummary(emptyJSON); got != "" {
			t.Fatalf("empty json body got %q, want empty", got)
		}
	})

	t.Run("preserves upstream text verbatim without regex redaction", func(t *testing.T) {
		t.Parallel()
		rawMsg := "Call to https://gw.example.com/v1/chat?model=gpt-5 rejected; trace=Bearer tok_abc_123, prefix=sk-proj-ab12****89"
		err := &sdk.APIError{
			StatusCode: http.StatusTooManyRequests,
			Code:       "rate_limit_exceeded",
			Message:    rawMsg,
		}
		want := "HTTP 429 · rate_limit_exceeded: " + rawMsg
		if got := UpstreamErrorSummary(err); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("truncates long multibyte UTF-8 strings cleanly at limit", func(t *testing.T) {
		t.Parallel()
		longMsg := strings.Repeat("错", 1000) // 3000 bytes
		err := &sdk.APIError{
			StatusCode: http.StatusInternalServerError,
			Code:       "internal_error",
			Message:    longMsg,
		}
		got := UpstreamErrorSummary(err)
		if !utf8.ValidString(got) {
			t.Fatalf("truncated summary is not valid UTF-8")
		}
		if !strings.HasSuffix(got, "…") {
			t.Fatalf("truncated summary %q missing ellipsis suffix", got)
		}
		if len(got) > maxUpstreamSummaryBytes+len("…") {
			t.Fatalf("truncated summary byte len = %d, exceeds bound", len(got))
		}
	})
}
