package grpctransport

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/rpc"
)

// The deployment mode does not change the answer a user gets: the channel
// answers and attributes a turn's catalog error the same whether it called
// the turn service in its own process or over the RPC.
func TestTurnErrorIsAnsweredAlikeInProcessAndOverRPC(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"client code", apperror.New(apperror.CodeACPAgentNotFound, nil)},
		{"declared dependency code", apperror.Wrap(apperror.CodeAgentProviderRateLimited, errors.New("api error 429"), nil)},
		{"5xx code over a dependency failure", apperror.Wrap(apperror.CodeWorkspaceUnreachable, errs.WrapDependency(errors.New("connection refused"), "dial workspace"), nil)},
	} {
		for path, deliver := range map[string]func(*testing.T, error) error{
			"start": startErrorOverTransport,
			"run":   runErrorOverTransport,
		} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				received := deliver(t, tc.err)
				local := errs.Analyze(context.Background(), tc.err)
				remote := errs.Analyze(context.Background(), received)
				localAnswer, localFault := errs.Answer(context.Background(), tc.err)
				remoteAnswer, remoteFault := errs.Answer(context.Background(), received)
				if remote.Reason != local.Reason || remoteFault != localFault || apperror.CodeOf(remoteAnswer) != apperror.CodeOf(localAnswer) {
					t.Fatalf("over RPC reason=%s answer=%s %s; in process reason=%s answer=%s %s",
						remote.Reason, apperror.CodeOf(remoteAnswer), remoteFault, local.Reason, apperror.CodeOf(localAnswer), localFault)
				}
				if !remote.Remote {
					t.Fatal("the error over RPC is not marked remote")
				}
			})
		}
	}

	// The server process's own failure is a dependency's failure to the
	// channel; its code is still the answer.
	failed := apperror.New(apperror.CodeRuntimeRunFailed, nil)
	answer, fault := errs.Answer(context.Background(), startErrorOverTransport(t, failed))
	if apperror.CodeOf(answer) != apperror.CodeRuntimeRunFailed || fault != apperror.FaultDependency {
		t.Fatalf("server failure over RPC = %s %s", apperror.CodeOf(answer), fault)
	}
}

// A refusal outside the catalog, such as a payload the server cannot read,
// refuses what the channel process sent and is not forwarded.
func TestNativeTurnRefusalIsNotForwarded(t *testing.T) {
	err := mapClientError(status.Error(codes.InvalidArgument, "invalid start turn payload"))
	if got := errs.FaultOf(err); got == apperror.FaultClient {
		t.Fatalf("native refusal attributed to the client: %v", err)
	}
}

// A canceled or expired call stays the received status. The caller's own
// context decides whether the caller ended it.
func TestCanceledTurnStaysAStatus(t *testing.T) {
	for _, code := range []codes.Code{codes.Canceled, codes.DeadlineExceeded} {
		err := startErrorOverTransport(t, statusError(code))
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s became a context error: %v", code, err)
		}
		if status.Code(rpc.Received(err)) != code {
			t.Fatalf("%s: received %v", code, rpc.Received(err))
		}
		// The server ended the call while the channel still waited.
		if got := errs.Analyze(context.Background(), err).Fault; got != apperror.FaultDependency {
			t.Fatalf("%s with a live caller: fault = %s, want dependency", code, got)
		}
		ended, cancel := context.WithCancel(context.Background())
		cancel()
		if got := errs.Analyze(ended, err).Fault; got != apperror.FaultCanceled {
			t.Fatalf("%s with an ended caller: fault = %s, want canceled", code, got)
		}
	}
}

// statusError is the context error the turn service returns for code; the
// server sends it as that code.
func statusError(code codes.Code) error {
	if code == codes.DeadlineExceeded {
		return context.DeadlineExceeded
	}
	return context.Canceled
}
