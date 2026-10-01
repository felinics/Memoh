package channelruntime

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/rpc"
)

// overWire returns err as a client receives it.
func overWire(t *testing.T, err error) error {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("%v is not a status", err)
	}
	return status.FromProto(st.Proto()).Err()
}

func TestChannelSentinelsSurviveBothEncodings(t *testing.T) {
	const cause = "telegram: getMe failed"
	for _, entry := range reasons {
		encodings := []struct {
			name     string
			wire     error
			wantText string
		}{
			{"envelope", entry.Status(""), entry.Err.Error()},
			{"envelope with adapter message", entry.Status(cause), cause},
			{"legacy bare reason", status.Error(entry.Code, entry.Reason), entry.Err.Error()},
			{"legacy with cause", safeChannelError(errors.Join(entry.Err, errors.New(cause))), entry.Err.Error() + "\n" + cause},
		}
		for _, encoding := range encodings {
			t.Run(entry.Reason+"/"+encoding.name, func(t *testing.T) {
				wire := overWire(t, encoding.wire)
				restored := restoreChannelError(wire)
				for _, other := range reasons {
					if got, want := errors.Is(restored, other.Err), errors.Is(other.Err, entry.Err); got != want {
						t.Fatalf("errors.Is(%v, %v) = %v", restored, other.Err, got)
					}
				}
				if got := restored.Error(); got != encoding.wantText {
					t.Fatalf("text = %q, want %q", got, encoding.wantText)
				}
				if !errors.Is(rpc.Received(restored), wire) {
					t.Fatal("received status is not on the chain")
				}
				if !errs.Analyze(context.Background(), restored).Remote {
					t.Fatal("restored sentinel is not marked remote")
				}
			})
		}
	}
}
