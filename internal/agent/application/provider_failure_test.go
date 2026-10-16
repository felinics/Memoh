package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/models"
)

func TestProviderFailureCode(t *testing.T) {
	t.Parallel()

	apiErr := func(status int, kind sdk.ErrorKind) error {
		return &sdk.APIError{Provider: "openai-completions", StatusCode: status, Kind: kind, Message: "SECRET provider message"}
	}
	refused := &url.Error{Op: "Post", URL: "http://127.0.0.1:1/v1/chat/completions", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}

	cases := []struct {
		name string
		err  error
		want apperror.Code
	}{
		{"rejected key", apiErr(401, sdk.KindAuthentication), apperror.CodeAgentProviderAuthFailed},
		{"key without access to the model", apiErr(403, sdk.KindPermissionDenied), apperror.CodeAgentProviderPermissionDenied},
		{"insufficient balance", apiErr(402, sdk.KindQuotaExhausted), apperror.CodeAgentProviderQuotaExhausted},
		{"exhausted quota answered with 429", apiErr(429, sdk.KindQuotaExhausted), apperror.CodeAgentProviderQuotaExhausted},
		{"rate limit", apiErr(429, sdk.KindRateLimited), apperror.CodeAgentProviderRateLimited},
		{"upstream 503", apiErr(503, sdk.KindServerError), apperror.CodeAgentProviderOverloaded},
		{"anthropic 529", apiErr(529, sdk.KindServerError), apperror.CodeAgentProviderOverloaded},
		{"overload reported inside the stream", apiErr(0, sdk.KindServerError), apperror.CodeAgentProviderOverloaded},
		{"bad request", apiErr(400, sdk.KindUnknown), apperror.CodeAgentProviderRequestRejected},
		{"model not found", apiErr(404, sdk.KindUnknown), apperror.CodeAgentProviderRequestRejected},
		{"not implemented", apiErr(501, sdk.KindUnknown), apperror.CodeAgentProviderRequestRejected},
		{"a kind a later SDK adds", apiErr(400, sdk.ErrorKind("content_filtered")), apperror.CodeAgentProviderRequestRejected},
		{"unclassified error inside the stream", apiErr(0, sdk.KindUnknown), ""},
		{"provider answer under the runtime's wrapping", fmt.Errorf("model call retries exhausted: %w", errs.WrapDependency(apiErr(429, sdk.KindRateLimited), "model stream")), apperror.CodeAgentProviderRateLimited},
		{"connection refused", refused, apperror.CodeAgentProviderUnreachable},
		{"connection refused under the runtime's wrapping", errs.WrapDependency(refused, "model stream"), apperror.CodeAgentProviderUnreachable},
		{"connection reset mid-stream", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, ""},
		{"truncated stream", fmt.Errorf("read: %w", io.ErrUnexpectedEOF), ""},
		{"stream ended before its terminal event", sdk.ErrStreamIncomplete, ""},
		{"status text without an APIError", errors.New("api error 401: Incorrect API key provided"), ""},
		{"no failure", nil, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := providerFailureCode(tc.err, true); got != tc.want {
				t.Fatalf("providerFailureCode(%v, true) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// Outside the model call, a refused connection names no provider: the request
// may have gone anywhere. A provider's answer still names its condition.
func TestProviderFailureCodeOutsideTheModelCall(t *testing.T) {
	t.Parallel()

	refused := &url.Error{Op: "Post", URL: "http://127.0.0.1:1/approve", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}
	for _, tc := range []struct {
		name string
		err  error
		want apperror.Code
	}{
		{"connection refused", errs.Wrap(refused, "approval handler"), ""},
		{"provider answer", errs.Wrap(&sdk.APIError{StatusCode: 401, Kind: sdk.KindAuthentication}, "approval handler"), apperror.CodeAgentProviderAuthFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := providerFailureCode(tc.err, false); got != tc.want {
				t.Fatalf("providerFailureCode(%v, false) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// A provider nobody listens for is reported as unreachable. The code depends
// on the *url.Error net/http returns surviving the SDK's wrapping, which the
// SDK does not promise, so each client type is dialed for real.
func TestProviderFailureCodeNamesARefusedConnectionUnreachable(t *testing.T) {
	t.Parallel()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	baseURL := "http://" + listener.Addr().String() + "/v1"
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	for _, clientType := range []models.ClientType{
		models.ClientTypeOpenAICompletions,
		models.ClientTypeOpenAIResponses,
		models.ClientTypeAnthropicMessages,
		models.ClientTypeGoogleGenerativeAI,
	} {
		t.Run(string(clientType), func(t *testing.T) {
			t.Parallel()
			model := models.NewSDKChatModel(models.SDKModelConfig{
				ModelID:    "unreachable-model",
				ClientType: string(clientType),
				APIKey:     "test-key",
				BaseURL:    baseURL,
			})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			failure := streamFailure(ctx, t, model)
			if got := providerFailureCode(failure, true); got != apperror.CodeAgentProviderUnreachable {
				t.Fatalf("providerFailureCode(%v) = %q, want %q", failure, got, apperror.CodeAgentProviderUnreachable)
			}
		})
	}
}

// streamFailure is the failure one model call reports, whether the provider
// returns it from DoStream or in the part stream.
func streamFailure(ctx context.Context, t *testing.T, model *sdk.Model) error {
	t.Helper()
	parts, err := model.Provider.DoStream(ctx, sdk.Request{
		Model:    model.ID,
		Messages: []sdk.Message{sdk.UserMessage("hello")},
	})
	if err != nil {
		return err
	}
	for part := range parts {
		if failed, ok := part.(*sdk.ErrorPart); ok {
			return failed.Error
		}
	}
	t.Fatal("model call against a closed port did not fail")
	return nil
}
