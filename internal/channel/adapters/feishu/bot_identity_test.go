package feishu

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestResolveBotOpenIDRecordsDiscoveryFailureAsOneEvent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	adapter := NewFeishuAdapter(slog.New(slog.NewJSONHandler(&buf, nil)))

	got := adapter.resolveBotOpenID(context.Background(), channel.ChannelConfig{ID: "cfg-1", Credentials: map[string]any{}})
	if got != "" {
		t.Fatalf("open id = %q, want empty when discovery fails", got)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("records = %d, want 1: %s", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], `"level":"WARN"`) || !strings.Contains(lines[0], `"fault"`) {
		t.Fatalf("record = %s, want a WARN event with fault", lines[0])
	}
}

func TestFeishuResponseFailuresAreReturnedWithoutLogging(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	adapter := NewFeishuAdapter(slog.New(slog.NewJSONHandler(&buf, nil)))
	cause := errors.New("network down")

	if err := adapter.handleResponse("cfg-1", nil, cause); !errors.Is(err, cause) {
		t.Fatalf("handleResponse error = %v, want the cause", err)
	}
	if err := adapter.handleReplyResponse("cfg-1", nil, cause); !errors.Is(err, cause) {
		t.Fatalf("handleReplyResponse error = %v, want the cause", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("adapter logged failures it returns: %s", buf.String())
	}
}
