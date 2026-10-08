package channelruntime

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/redact"
)

func TestServerRedactsChannelCause(t *testing.T) {
	const secret = "channel-encode-bot-token" //nolint:gosec // test fixture, not a credential
	redact.SetSecrets("channel-encode", secret)
	t.Cleanup(func() { redact.SetSecrets("channel-encode") })

	for _, entry := range reasons {
		wire := overWire(t, safeChannelError(errors.Join(entry.Err, errors.New("rejected "+secret+" at https://user:pass@api.example.test/bot"))))
		restored := restoreChannelError(wire)
		if !errors.Is(restored, entry.Err) {
			t.Fatalf("%s: %v lost the sentinel", entry.Reason, restored)
		}
		for _, leaked := range []string{secret, "user:pass"} {
			if strings.Contains(restored.Error(), leaked) || strings.Contains(status.Convert(wire).Message(), leaked) {
				t.Fatalf("%s: %q leaks %q", entry.Reason, restored.Error(), leaked)
			}
		}
	}
}
