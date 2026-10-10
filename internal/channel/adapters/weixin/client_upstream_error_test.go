package weixin

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
)

// attrValue returns the value of key among the error's attributes.
func attrValue(t *testing.T, err error, key string) (any, bool) {
	t.Helper()
	for _, attr := range errs.Analyze(context.Background(), err).Attrs {
		if attr.Key == key {
			return attr.Value.Any(), true
		}
	}
	return nil, false
}

func TestSendMessageRejectionKeepsUpstreamTextOutOfTheError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret":-3,"errmsg":"ilink secret rejection"}`))
	}))
	defer server.Close()

	client := NewClient(slog.Default())
	err := client.SendMessage(context.Background(), adapterConfig{BaseURL: server.URL, Token: "tok"}, SendMessageRequest{})
	if err == nil {
		t.Fatal("SendMessage returned nil, want the upstream rejection")
	}
	if strings.Contains(err.Error(), "ilink secret rejection") {
		t.Fatalf("error carries the upstream text: %v", err)
	}
	if got, ok := attrValue(t, err, "ret"); !ok || got != int64(-3) {
		t.Fatalf("ret attr = %v (present %t), want -3", got, ok)
	}
	if fault := errs.FaultOf(err); fault != apperror.FaultDependency {
		t.Fatalf("fault = %q, want dependency", fault)
	}
}

func TestNotifyLifecycleRejectionKeepsUpstreamTextOutOfTheError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret":-14,"errmsg":"ilink secret lifecycle"}`))
	}))
	defer server.Close()

	client := NewClient(slog.Default())
	err := client.NotifyStart(context.Background(), adapterConfig{BaseURL: server.URL, Token: "tok"})
	if err == nil {
		t.Fatal("NotifyStart returned nil, want the upstream rejection")
	}
	if strings.Contains(err.Error(), "ilink secret lifecycle") {
		t.Fatalf("error carries the upstream text: %v", err)
	}
	if got, ok := attrValue(t, err, "ret"); !ok || got != int64(-14) {
		t.Fatalf("ret attr = %v (present %t), want -14", got, ok)
	}
	if got, ok := attrValue(t, err, "endpoint"); !ok || got != "ilink/bot/msg/notifystart" {
		t.Fatalf("endpoint attr = %v (present %t)", got, ok)
	}
}

func TestSendMessageRejectionPutsTheEndpointInTheMessage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/ilink/bot/sendmessage" {
			t.Fatalf("path = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret":1}`))
	}))
	defer server.Close()

	client := NewClient(slog.Default())
	err := client.SendMessage(context.Background(), adapterConfig{BaseURL: server.URL, Token: "tok"}, SendMessageRequest{})
	if err == nil || !strings.Contains(err.Error(), "weixin sendmessage failed") {
		t.Fatalf("error = %v, want the constant sendmessage failure", err)
	}
}
