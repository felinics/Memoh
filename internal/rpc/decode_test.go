package rpc_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/rpc"
)

var (
	errDecodeBusy        = errors.New("busy")
	errDecodeUnavailable = errors.New("peer unavailable")
	decodeReasons        = rpc.Reasons{{Err: errDecodeBusy, Reason: "test.busy", Code: codes.Aborted, Message: "busy"}}
	decodeByCode         = map[codes.Code]error{codes.Unavailable: errDecodeUnavailable}
)

func TestDecodeRestoresInOrder(t *testing.T) {
	tests := []struct {
		name     string
		received error
		check    func(error) bool
	}{
		{"package reason", decodeReasons[0].Status(""), func(err error) bool { return errors.Is(err, errDecodeBusy) }},
		{"catalog code", rpc.AnswerStatus(context.Background(), apperror.New(apperror.CodeBotNameTaken, nil)), func(err error) bool {
			return apperror.CodeOf(err) == apperror.CodeBotNameTaken
		}},
		// A reason outranks the code: an Unavailable envelope with a catalog
		// reason restores the catalog error, not the code's sentinel.
		{"reason before code", rpc.AnswerStatus(context.Background(), apperror.New(apperror.CodeWorkspaceUnreachable, nil)), func(err error) bool {
			return apperror.CodeOf(err) == apperror.CodeWorkspaceUnreachable && !errors.Is(err, errDecodeUnavailable)
		}},
		{"code", status.Error(codes.Unavailable, "connection refused"), func(err error) bool { return errors.Is(err, errDecodeUnavailable) }},
		{"other code", status.Error(codes.Internal, "boom"), func(err error) bool { return status.Code(err) == codes.Internal }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := rpc.Decode(tt.received, decodeReasons, decodeByCode)
			if !tt.check(err) {
				t.Fatalf("decoded %v", err)
			}
			if !errs.Analyze(context.Background(), err).Remote {
				t.Fatal("decoded error is not marked remote")
			}
			if status.Code(rpc.Received(err)) != status.Code(tt.received) {
				t.Fatalf("received status not on the chain: %v", rpc.Received(err))
			}
		})
	}
}

// The status message identifies nothing: a status whose message names a
// sentinel restores only what its reason or code gives.
func TestDecodeDoesNotReadTheMessage(t *testing.T) {
	err := rpc.Decode(status.Error(codes.Aborted, "busy"), decodeReasons, decodeByCode)
	if errors.Is(err, errDecodeBusy) {
		t.Fatalf("restored %v from the message", err)
	}
}

// Canceled and DeadlineExceeded stay received statuses. Only the caller's own
// context says whether the caller ended the call.
func TestDecodeKeepsCancellationAStatus(t *testing.T) {
	for _, code := range []codes.Code{codes.Canceled, codes.DeadlineExceeded} {
		err := rpc.Decode(status.Error(code, "ended"), decodeReasons, decodeByCode)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s became a context error: %v", code, err)
		}
		if status.Code(err) != code {
			t.Fatalf("%s: status = %v", code, status.Code(err))
		}
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

func TestDecodePassesErrorsThatWereNotReceived(t *testing.T) {
	if err := rpc.Decode(nil, decodeReasons, decodeByCode); err != nil {
		t.Fatalf("nil: %v", err)
	}
	if err := rpc.Decode(io.EOF, decodeReasons, decodeByCode); err != io.EOF { //nolint:errorlint // identity is the point
		t.Fatalf("io.EOF: %v", err)
	}
}

// A sentinel restored by code reads as the sentinel and the received message;
// the rendered chain of any restored error carries the received status.
func TestDecodedText(t *testing.T) {
	err := rpc.Decode(status.Error(codes.Unavailable, "connection refused"), decodeReasons, decodeByCode)
	if err.Error() != "peer unavailable: connection refused" {
		t.Fatalf("text = %q", err.Error())
	}
	restored := rpc.Decode(decodeReasons[0].Status(""), decodeReasons, decodeByCode)
	if restored.Error() != "busy" {
		t.Fatalf("restored text = %q", restored.Error())
	}
	if text := errs.Text(restored); !strings.Contains(text, "code = Aborted") {
		t.Fatalf("rendered chain %q does not carry the received status", text)
	}
}

func TestForwardMarksOnlyCatalogErrors(t *testing.T) {
	refusal := rpc.AnswerStatus(context.Background(), apperror.New(apperror.CodeBotNameTaken, nil))
	forwarded := rpc.Forward(rpc.Decode(refusal, nil, nil))
	if got := errs.FaultOf(forwarded); got != apperror.FaultClient {
		t.Fatalf("forwarded catalog refusal: fault = %s, want client", got)
	}
	if got := errs.FaultOf(rpc.Decode(refusal, nil, nil)); got != apperror.FaultServer {
		t.Fatalf("catalog refusal of this process's request: fault = %s, want server", got)
	}
	native := rpc.Decode(status.Error(codes.InvalidArgument, "invalid payload"), nil, nil)
	if rpc.Forward(native) != native { //nolint:errorlint // identity is the point
		t.Fatal("a native status was forwarded")
	}
}
