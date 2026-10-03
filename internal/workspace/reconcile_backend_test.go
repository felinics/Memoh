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

	ctr "github.com/felinics/memoh/internal/container"
)

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
