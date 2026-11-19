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

	t.Run("non-APIError returns empty", func(t *testing.T) {
		t.Parallel()
		for _, err := range []error{
			nil,
			errors.New("plain error"),
			&url.Error{Op: "Post", URL: "https://api.example.com/v1", Err: errors.New("connection refused")},
		} {
			if got := UpstreamErrorSummary(err); got != "" {
				t.Fatalf("UpstreamErrorSummary(%v) = %q, want empty", err, got)
			}
		}
	})

	t.Run("401 and KindAuthentication always return empty", func(t *testing.T) {
		t.Parallel()
		for _, err := range []error{
			&sdk.APIError{StatusCode: http.StatusUnauthorized, Kind: sdk.KindUnknown, Code: "invalid_api_key", Message: "Incorrect API key: sk-live-secret"},
			&sdk.APIError{StatusCode: http.StatusForbidden, Kind: sdk.KindAuthentication, Code: "auth_error", Message: "Bad key"},
			&sdk.APIError{StatusCode: http.StatusBadRequest, Kind: sdk.KindAuthentication, Message: "Token expired", RequestID: "req_auth"},
		} {
			if got := UpstreamErrorSummary(err); got != "" {
				t.Fatalf("UpstreamErrorSummary(%v) = %q, want empty", err, got)
			}
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

	t.Run("redacts URLs and common API tokens", func(t *testing.T) {
		t.Parallel()
		err := &sdk.APIError{
			StatusCode: http.StatusTooManyRequests,
			Code:       "rate_limit_exceeded",
			Message: "Call to https://user:secret@gw.example.com/v1/chat?api_key=xyz#frag rejected; " +
				"auth=Bearer tok_secret_123, key1=sk-proj-abcdef123456, key2=AIzaSyAbcdefghijklmnop",
		}
		got := UpstreamErrorSummary(err)
		for _, leaked := range []string{"user:secret", "api_key=xyz", "#frag", "tok_secret_123", "abcdef123456", "SyAbcdefghijklmnop"} {
			if strings.Contains(got, leaked) {
				t.Fatalf("sanitized summary %q leaked %q", got, leaked)
			}
		}
		for _, expected := range []string{"https://gw.example.com/v1/chat", "Bearer [redacted]", "sk-[redacted]", "AIza[redacted]"} {
			if !strings.Contains(got, expected) {
				t.Fatalf("sanitized summary %q missing expected redaction %q", got, expected)
			}
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
