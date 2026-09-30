package providerfail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/chatgptplan"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestChatGPTDiagnosticsStayInLogsAcrossHTTPAndSSE(t *testing.T) {
	for _, test := range []struct {
		name, body, shape string
		status            int
	}{
		{"http", `{"error":{"code":"subscription_sharing_unsupported_capability","param":"tools[0]","message":"SECRET"}}`, "error_object", 400},
		{"stream_error", "data: {\"type\":\"error\",\"code\":\"subscription_sharing_unsupported_capability\",\"param\":\"tools[0]\",\"message\":\"SECRET\"}\n\n", "error", 200},
		{"nested_stream_error", "data: {\"type\":\"error\",\"error\":{\"code\":\"subscription_sharing_unsupported_capability\",\"param\":\"tools[0]\",\"message\":\"SECRET\"}}\n\n", "error", 200},
		{"stream", "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"subscription_sharing_unsupported_capability\",\"param\":\"tools[0]\",\"message\":\"SECRET\"}}}\n\n", "response.failed", 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := chatgptplan.NewProvider("credential", nil, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"upstream-request"}}, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})})
			_, err := p.DoGenerate(context.Background(), sdk.Request{Model: "test", Messages: []sdk.Message{sdk.UserMessage("Hi")}})
			var diagnostic *chatgptplan.UpstreamError
			if !errors.As(err, &diagnostic) || diagnostic.Status != test.status || diagnostic.Code != "subscription_sharing_unsupported_capability" || diagnostic.Param != "tools[0]" || diagnostic.RequestID != "upstream-request" || diagnostic.Shape != test.shape {
				t.Fatalf("missing metadata: %+v err=%v", diagnostic, err)
			}
			var logs bytes.Buffer
			mapped := ChatGPT(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)), err)
			public, ok := apperror.ProblemFrom(mapped, "local-request")
			if !ok || public.Code != string(apperror.CodeChatGPTCapabilityUnsupported) {
				t.Fatalf("public error = %+v", public)
			}
			wire, _ := json.Marshal(public)
			for _, private := range []string{"SECRET", "upstream-request", "tools[0]", "credential", "subscription_sharing_unsupported_capability"} {
				if strings.Contains(string(wire), private) || strings.Contains(err.Error(), private) {
					t.Fatalf("private diagnostic escaped: %s", private)
				}
			}
			if strings.Contains(logs.String(), "SECRET") || strings.Contains(logs.String(), "credential") || !strings.Contains(logs.String(), "upstream-request") || !strings.Contains(logs.String(), "tools[0]") {
				t.Fatalf("incorrect diagnostic log: %s", logs.String())
			}
		})
	}
}
