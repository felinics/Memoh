package rpc

import (
	"context"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
)

// TestStatusCodeKeepsFaultClass guards the one property the HTTP to gRPC
// mapping must keep: a peer that does not know the reason reads the code's
// class, so every 4xx status lands on a client code and every 5xx status on a
// server code. 499 lands on Canceled, which is a cancellation at a boundary
// whose caller has ended.
func TestStatusCodeKeepsFaultClass(t *testing.T) {
	t.Parallel()
	for s := 400; s <= 599; s++ {
		if s == 499 {
			continue
		}
		want := apperror.FaultServer
		if s < http.StatusInternalServerError {
			want = apperror.FaultClient
		}
		code := statusCodeForHTTP(s)
		if got := errs.FaultOf(status.Error(code, "")); got != want {
			t.Errorf("HTTP %d -> %s: fault %s, want %s", s, code, got, want)
		}
	}
	if code := statusCodeForHTTP(499); code != codes.Canceled {
		t.Fatalf("HTTP 499 -> %s, want Canceled", code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := errs.Analyze(ctx, status.Error(statusCodeForHTTP(499), "")).Fault; got != apperror.FaultCanceled {
		t.Fatalf("HTTP 499 under an ended caller: fault %s, want canceled", got)
	}
}
