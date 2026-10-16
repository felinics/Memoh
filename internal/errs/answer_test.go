package errs

import (
	"context"
	stderrors "errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
)

func TestAnswer(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	restored := func(code apperror.Code, fault string) error {
		return Remote(stderrors.Join(apperror.New(code, nil), remoteStatus(t, codes.Internal, "remote", string(code), fault)))
	}
	outer := apperror.New(codeClientBare, nil)
	for _, tc := range []struct {
		name      string
		ctx       context.Context
		err       error
		code      apperror.Code
		fault     apperror.Fault
		sameError error
	}{
		{"caller canceled", canceled, Wrap(apperror.Wrap(codeServer, context.Canceled, nil), "call"), apperror.CodeCanceled, apperror.FaultCanceled, nil},
		{"cancellation the caller did not cause", context.Background(), Wrap(context.Canceled, "call"), apperror.CodeInternal, apperror.FaultServer, nil},
		{"outermost catalog error", context.Background(), fmt.Errorf("handler: %w", outer), codeClientBare, apperror.FaultClient, outer},
		{"5xx catalog error", context.Background(), apperror.Wrap(codeServer, New("dial"), nil), codeServer, apperror.FaultServer, nil},
		{"remote refused this process", context.Background(), Wrap(restored(codeClientBare, "client"), "call"), apperror.CodeInternal, apperror.FaultServer, nil},
		{"forwarded refusal", context.Background(), Wrap(Forwarded(restored(codeClientBare, "client")), "call"), codeClientBare, apperror.FaultClient, nil},
		{"remote failure", context.Background(), restored(codeServer, "server"), codeServer, apperror.FaultDependency, nil},
		{"code outside the catalog", context.Background(), apperror.New("not.registered", nil), apperror.CodeInternal, apperror.FaultServer, nil},
		{"native client status", context.Background(), Wrap(status.Error(codes.NotFound, "runtime not found"), "lookup"), apperror.CodeHTTPBadRequest, apperror.FaultClient, nil},
		{"native server status", context.Background(), status.Error(codes.Unavailable, "down"), apperror.CodeInternal, apperror.FaultServer, nil},
		{"dependency without a public error", context.Background(), NewDependency("down"), apperror.CodeInternal, apperror.FaultDependency, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, fault := Answer(tc.ctx, tc.err)
			if apperror.CodeOf(got) != tc.code || fault != tc.fault {
				t.Fatalf("Answer = %s %s, want %s %s", apperror.CodeOf(got), fault, tc.code, tc.fault)
			}
			if tc.sameError != nil && got != tc.sameError { //nolint:errorlint // identity: the chain's own node is the answer.
				t.Fatalf("Answer returned %p, want the chain's node %p", got, tc.sameError)
			}
			if fault != Analyze(tc.ctx, tc.err).Fault {
				t.Fatalf("Answer fault %s differs from Analyze", fault)
			}
		})
	}
	if got, fault := Answer(context.Background(), nil); got != nil || fault != "" {
		t.Fatalf("Answer(nil) = %v %s", got, fault)
	}
}
