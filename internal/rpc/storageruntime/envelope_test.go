package storageruntime

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/media"
	intrpc "github.com/felinics/memoh/internal/rpc"
	"github.com/felinics/memoh/internal/storage"
)

// A storage failure crosses the RPC as its sentinel, with the server's fault,
// and the server records the cause rather than logging it itself.
func TestStorageFailuresCrossAsSentinels(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		sentinel error
	}{
		{name: "missing", err: os.ErrNotExist, sentinel: os.ErrNotExist},
		{name: "too large", err: media.ErrAssetTooLarge, sentinel: media.ErrAssetTooLarge},
		{name: "unavailable", err: media.ErrProviderUnavailable, sentinel: media.ErrProviderUnavailable},
		{name: "access path", err: storage.ErrAccessPathUnavailable, sentinel: storage.ErrAccessPathUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := (&Server{}).mapError(context.Background(), "test", tc.err)
			restored := mapClientError(wire)
			if !errors.Is(restored, tc.sentinel) {
				t.Fatalf("restored %v, want %v", restored, tc.sentinel)
			}
			if reason, ok := intrpc.ReasonOf(restored); !ok || !strings.HasPrefix(reason, "storage.") {
				t.Fatalf("reason = %q %v, want a storage reason", reason, ok)
			}
		})
	}
	internal := mapClientError((&Server{}).mapError(context.Background(), "test", errors.New("disk full")))
	if strings.Contains(internal.Error(), "disk full") {
		t.Fatalf("internal cause crossed the RPC: %v", internal)
	}
}
