package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"

	"github.com/felinics/memoh/internal/botworkspace"
	"github.com/felinics/memoh/internal/config"
	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type unreadableBridgeService struct{ legacyRouteTestService }

func (*unreadableBridgeService) MCPClient(context.Context, string) (*bridge.Client, error) {
	return nil, errors.New("bridge refused")
}

func TestTeardownPreservingData(t *testing.T) {
	const botID = "00000000-0000-0000-0000-000000000001"
	svc := &unreadableBridgeService{}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{DataRoot: t.TempDir()})

	// The container is already gone: nothing to export, the removal is done.
	if err := m.Teardown(context.Background(), botID, true); err != nil {
		t.Fatalf("Teardown() of a missing container = %v", err)
	}

	svc.created = true
	deletes := svc.deleteCalls
	err := m.Teardown(context.Background(), botID, true)
	var step *botworkspace.StepError
	if !errors.As(err, &step) || step.Retryable {
		t.Fatalf("Teardown() with a failing export = %v, want a non-retryable failure", err)
	}
	if svc.deleteCalls != deletes {
		t.Fatal("container deleted without its data exported")
	}
}

func TestIsTransientClassifiesByErrorChain(t *testing.T) {
	tests := map[string]struct {
		err  error
		want bool
	}{
		"deadline":            {err: fmt.Errorf("pull: %w", context.DeadlineExceeded), want: true},
		"runtime conflict":    {err: errors.Join(ctr.ErrConflict, errors.New("operation in flight")), want: true},
		"runtime unavailable": {err: errors.Join(ctr.ErrUnavailable, ctr.ErrRuntime, errors.New("daemon down")), want: true},
		"registry transport": {
			err:  &url.Error{Op: "Get", URL: "https://registry.example/v2/", Err: &net.DNSError{Err: "no such host", Name: "registry.example", IsNotFound: true}},
			want: true,
		},
		"truncated stream":  {err: fmt.Errorf("read layer: %w", io.ErrUnexpectedEOF), want: true},
		"connection reset":  {err: errors.Join(ctr.ErrRuntime, os.NewSyscallError("read", syscall.ECONNRESET)), want: true},
		"connection refuse": {err: fmt.Errorf("dial: %w", syscall.ECONNREFUSED), want: true},
		"missing image": {
			err:  errors.Join(ctr.ErrNotFound, errors.New("manifest for memoh/whereof:latest not found")),
			want: false,
		},
		"runtime failure mentioning timeout": {
			err:  errors.Join(ctr.ErrRuntime, errors.New("invalid config: pull_timeout must be positive")),
			want: false,
		},
		"nil": {err: nil, want: false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := isTransient(tt.err); got != tt.want {
				t.Fatalf("isTransient(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
