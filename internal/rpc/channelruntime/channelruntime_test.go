package channelruntime

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/rpc"
)

// TestSafeChannelErrorRoundTripsSentinelAndCause pins the split-mode error
// contract: sentinel identity travels as the envelope reason and the original
// error text (platform-side cause included) is restored as the adapter
// message, matching what the pre-split in-process path surfaced to operators.
func TestSafeChannelErrorRoundTripsSentinelAndCause(t *testing.T) {
	wireErr := safeChannelError(errors.Join(channel.ErrEnableChannelFailed, errors.New("adapter cause")))
	if got := status.Code(wireErr); got != codes.FailedPrecondition {
		t.Fatalf("code = %v", got)
	}
	if got := status.Convert(wireErr).Message(); strings.Contains(got, "adapter cause") {
		t.Fatalf("message = %q, want the fixed message", got)
	}
	if reason, ok := rpc.ReasonOf(wireErr); !ok || reason != reasonEnableFailed {
		t.Fatalf("reason = %q, %v", reason, ok)
	}

	restored := restoreChannelError(wireErr)
	if !errors.Is(restored, channel.ErrEnableChannelFailed) {
		t.Fatalf("restored error lost sentinel identity: %v", restored)
	}
	if !strings.Contains(restored.Error(), "adapter cause") {
		t.Fatalf("restored error lost cause text: %v", restored)
	}
}

// Servers sent a channel sentinel as its reason token in the status message,
// alone or followed by "\x1f" and the cause, before the envelope. Without the
// envelope the status is returned as received.
func TestRestoreChannelErrorIgnoresPreEnvelopeMessage(t *testing.T) {
	for _, entry := range reasons {
		for _, message := range []string{entry.Reason, entry.Reason + "\x1f" + "telegram: getMe failed"} {
			wire := status.Error(entry.Code, message)
			restored := restoreChannelError(wire)
			for _, other := range reasons {
				if errors.Is(restored, other.Err) {
					t.Fatalf("%q: restored %v", message, other.Err)
				}
			}
			if restored != wire { //nolint:errorlint // the unchanged error itself is expected
				t.Fatalf("%q: got %v, want the received status", message, restored)
			}
		}
	}
}

func TestSafeChannelErrorLeavesUnknownCauseForRuntimeSanitization(t *testing.T) {
	cause := errors.New("private database detail")
	if got := safeChannelError(cause); !errors.Is(got, cause) {
		t.Fatalf("error = %v", got)
	}
}

// A send or reaction that fails with a channel sentinel crosses as its
// reason; any other failure keeps the adapter's text.
func TestDeliveryErrorKeepsChannelSentinels(t *testing.T) {
	sent := fmt.Errorf("resolve config: %w", channel.ErrChannelConfigNotFound)
	restored := restoreChannelError(overWire(t, deliveryError(sent)))
	if !errors.Is(restored, channel.ErrChannelConfigNotFound) {
		t.Fatalf("got %v, want the channel sentinel", restored)
	}
	adapter := errors.New("telegram: chat not found")
	if public := deliveryError(adapter); !errors.Is(public, adapter) || public.Error() != adapter.Error() {
		t.Fatalf("got %v, want the adapter error marked public", public)
	}
	if deliveryError(nil) != nil {
		t.Fatal("deliveryError(nil) is not nil")
	}
}
