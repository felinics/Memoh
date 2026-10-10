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

func TestImagePullErrorSeparatesMissingImageFromUnreachableRegistry(t *testing.T) {
	tests := map[string]struct {
		err  error
		want error
	}{
		"image missing":    {err: errors.Join(ctr.ErrNotFound, errors.New("registry: manifest unknown")), want: botworkspace.ErrImageNotFound},
		"invalid name":     {err: errors.Join(ctr.ErrInvalidArgument, errors.New("invalid reference format")), want: botworkspace.ErrImageNotFound},
		"runtime down":     {err: errors.Join(ctr.ErrUnavailable, ctr.ErrRuntime, errors.New("daemon down")), want: botworkspace.ErrImageRegistryUnavailable},
		"connection reset": {err: errors.Join(ctr.ErrRuntime, os.NewSyscallError("read", syscall.ECONNRESET)), want: botworkspace.ErrImageRegistryUnavailable},
		"dns failure": {
			err:  errors.Join(ctr.ErrRuntime, &url.Error{Op: "Get", URL: "https://registry.example/v2/", Err: &net.DNSError{Err: "no such host", Name: "registry.example", IsNotFound: true}}),
			want: botworkspace.ErrImageRegistryUnavailable,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := imagePullError(tt.err)
			if !errors.Is(got, tt.want) || !errors.Is(got, tt.err) {
				t.Fatalf("imagePullError = %v, want %v with the cause kept", got, tt.want)
			}
		})
	}
	for name, err := range map[string]error{
		"budget spent": fmt.Errorf("pull: %w", context.DeadlineExceeded),
		"canceled":     context.Canceled,
		"other":        errors.Join(ctr.ErrRuntime, errors.New("unpack failed")),
	} {
		t.Run(name, func(t *testing.T) {
			got := imagePullError(err)
			if errors.Is(got, botworkspace.ErrImageNotFound) || errors.Is(got, botworkspace.ErrImageRegistryUnavailable) {
				t.Fatalf("imagePullError(%v) = %v, want untagged", err, got)
			}
		})
	}
}
