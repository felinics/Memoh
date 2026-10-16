package misskey

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/errs"
)

// misskeyStub returns an adapter config and a parse result pointing at a stub
// that refuses every request with body.
func misskeyStub(t *testing.T, status int, body string) (Config, channel.ChannelConfig) {
	t.Helper()
	cfg, _ := withMisskeyHTTPStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	mkCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	return mkCfg, cfg
}

func TestAPIFailureKeepsUpstreamBodyOutOfTheError(t *testing.T) {
	t.Parallel()

	mkCfg, _ := misskeyStub(t, http.StatusForbidden, "misskey secret refusal")

	_, err := getMe(context.Background(), mkCfg)
	if err == nil {
		t.Fatal("getMe returned nil, want the upstream refusal")
	}
	if strings.Contains(err.Error(), "misskey secret refusal") {
		t.Fatalf("error carries the upstream body: %v", err)
	}
	if !strings.Contains(err.Error(), "misskey api request failed") {
		t.Fatalf("error = %v, want the constant api failure", err)
	}
	var status int64
	var endpoint string
	var found bool
	for _, attr := range errs.Analyze(context.Background(), err).Attrs {
		switch attr.Key {
		case "status":
			status, found = attr.Value.Int64(), true
		case "endpoint":
			endpoint = attr.Value.String()
		}
	}
	if !found || status != http.StatusForbidden {
		t.Fatalf("status attr = %d (present %t), want 403", status, found)
	}
	if endpoint != "i" {
		t.Fatalf("endpoint attr = %q, want i", endpoint)
	}
	if fault := errs.FaultOf(err); fault != apperror.FaultDependency {
		t.Fatalf("fault = %q, want dependency", fault)
	}
}

func TestSendKeepsTheUpstreamBodyOutOfTheErrorAndTheLog(t *testing.T) {
	t.Parallel()

	_, cfg := misskeyStub(t, http.StatusUnauthorized, `{"error":{"message":"misskey secret token"}}`)

	var buf bytes.Buffer
	adapter := NewMisskeyAdapter(slog.New(slog.NewJSONHandler(&buf, nil)))
	err := adapter.Send(context.Background(), cfg, channel.PreparedOutboundMessage{
		Target:  "note-source",
		Message: channel.PreparedMessage{Message: channel.Message{Text: "hello"}},
	})
	if err == nil {
		t.Fatal("Send returned nil, want the upstream refusal")
	}
	if strings.Contains(err.Error(), "misskey secret token") {
		t.Fatalf("error carries the upstream text: %v", err)
	}
	if strings.Contains(buf.String(), "misskey secret token") {
		t.Fatalf("log carries the upstream text: %s", buf.String())
	}
	if fault := errs.FaultOf(err); fault != apperror.FaultDependency {
		t.Fatalf("fault = %q, want dependency", fault)
	}
}
