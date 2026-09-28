package channelruntime

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/redact"
)

// legacyRestore is the decoding of a client built before the envelope: it
// matches the status message against the reason tokens.
func legacyRestore(err error) error {
	message := status.Convert(err).Message()
	for _, entry := range reasons {
		if message == entry.Reason || strings.HasPrefix(message, entry.Reason+reasonDetailSep) {
			return entry.Err
		}
	}
	return err
}

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

// A client built before the envelope finds no reason token in the fixed
// status message and keeps the raw status, without the sentinel.
func TestLegacyClientLosesChannelSentinel(t *testing.T) {
	for _, entry := range reasons {
		if !errors.Is(legacyRestore(status.Error(entry.Code, entry.Reason)), entry.Err) {
			t.Fatalf("%s: the replica does not read the legacy encoding", entry.Reason)
		}
		wire := overWire(t, safeChannelError(errors.Join(entry.Err, errors.New("telegram: getMe failed"))))
		restored := legacyRestore(wire)
		for _, other := range reasons {
			if errors.Is(restored, other.Err) {
				t.Fatalf("%s: legacy client restored %v", entry.Reason, other.Err)
			}
		}
		if status.Code(restored) != entry.Code || status.Convert(restored).Message() != entry.Message {
			t.Fatalf("%s: legacy error = %v, want the raw status", entry.Reason, restored)
		}
	}
}
