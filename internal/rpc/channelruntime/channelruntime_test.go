package channelruntime

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/channel"
)

// TestSafeChannelErrorRoundTripsSentinelAndCause pins the split-mode error
// contract: sentinel identity travels as a stable reason token and the
// original error text (platform-side cause included) is restored verbatim,
// matching what the pre-split in-process path surfaced to operators.
func TestSafeChannelErrorRoundTripsSentinelAndCause(t *testing.T) {
	wireErr := safeChannelError(errors.Join(channel.ErrEnableChannelFailed, errors.New("adapter cause")))
	if got := status.Code(wireErr); got != codes.FailedPrecondition {
		t.Fatalf("code = %v", got)
	}
	if got := status.Convert(wireErr).Message(); !strings.HasPrefix(got, reasonEnableFailed+reasonDetailSep) {
		t.Fatalf("message = %q", got)
	}

	restored := restoreChannelError(wireErr)
	if !errors.Is(restored, channel.ErrEnableChannelFailed) {
		t.Fatalf("restored error lost sentinel identity: %v", restored)
	}
	if !strings.Contains(restored.Error(), "adapter cause") {
		t.Fatalf("restored error lost cause text: %v", restored)
	}
}

// TestRestoreChannelErrorBareReason keeps compatibility with peers that
// send the reason token alone.
func TestRestoreChannelErrorBareReason(t *testing.T) {
	restored := restoreChannelError(status.Error(codes.NotFound, reasonConfigNotFound))
	if !errors.Is(restored, channel.ErrChannelConfigNotFound) {
		t.Fatalf("restored error = %v", restored)
	}
}

func TestSafeChannelErrorLeavesUnknownCauseForRuntimeSanitization(t *testing.T) {
	cause := errors.New("private database detail")
	if got := safeChannelError(cause); !errors.Is(got, cause) {
		t.Fatalf("error = %v", got)
	}
}
