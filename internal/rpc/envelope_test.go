package rpc_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/redact"
	"github.com/felinics/memoh/internal/rpc"
)

var (
	errEnvelopeFirst  = errors.New("first sentinel")
	errEnvelopeSecond = errors.New("second sentinel")
)

var envelopeTestReasons = rpc.Reasons{
	{Err: errEnvelopeFirst, Reason: "test.first", Code: codes.NotFound, Message: "first failed"},
	{Err: errEnvelopeSecond, Reason: "test.second", Code: codes.Aborted, Message: "second failed"},
}

// overWire returns err as a client receives it: the status marshaled to its
// proto form and read back.
func overWire(t *testing.T, err error) error {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("%v is not a status", err)
	}
	return status.FromProto(st.Proto()).Err()
}

func TestEnvelopeRoundTripsEverySentinel(t *testing.T) {
	for _, entry := range envelopeTestReasons {
		t.Run(entry.Reason, func(t *testing.T) {
			wire := overWire(t, entry.Status(""))
			if got := status.Code(wire); got != entry.Code {
				t.Fatalf("code = %v, want %v", got, entry.Code)
			}
			if got := status.Convert(wire).Message(); got != entry.Message {
				t.Fatalf("message = %q, want %q", got, entry.Message)
			}
			if reason, ok := rpc.ReasonOf(wire); !ok || reason != entry.Reason {
				t.Fatalf("reason = %q, %v", reason, ok)
			}
			restored := envelopeTestReasons.Decode(wire)
			if restored == nil {
				t.Fatal("envelope not decoded")
			}
			for _, other := range envelopeTestReasons {
				if got, want := errors.Is(restored, other.Err), errors.Is(other.Err, entry.Err); got != want {
					t.Fatalf("errors.Is(%v, %v) = %v", restored, other.Err, got)
				}
			}
			if restored.Error() != entry.Err.Error() {
				t.Fatalf("restored text = %q", restored.Error())
			}
			if !errors.Is(rpc.Received(restored), wire) || !errors.Is(restored, wire) {
				t.Fatal("received status is not on the chain")
			}
			if reason, ok := rpc.ReasonOf(restored); !ok || reason != entry.Reason {
				t.Fatalf("reason of the restored error = %q, %v", reason, ok)
			}
			if !errs.Analyze(context.Background(), restored).Remote {
				t.Fatal("restored error is not marked remote")
			}
			// Both codes refuse the request, so the server writes client.
			if got := errorInfo(t, wire).GetMetadata()[rpc.MetadataFault]; got != string(apperror.FaultClient) {
				t.Fatalf("fault = %q, want client", got)
			}
		})
	}
	unknown := rpc.Reason{Reason: "test.unknown", Code: codes.Unknown, Message: "failed"}.Status("adapter text")
	if got := errorInfo(t, unknown).GetMetadata()[rpc.MetadataFault]; got != string(apperror.FaultServer) {
		t.Fatalf("unknown code fault = %q, want server", got)
	}
}

func TestEnvelopeDecodeIgnoresOtherErrors(t *testing.T) {
	for name, err := range map[string]error{
		"plain status":   status.Error(codes.NotFound, "test.first"),
		"plain error":    errors.New("test.first"),
		"unknown reason": rpc.Reason{Reason: "test.other", Code: codes.NotFound, Message: "x"}.Status(""),
	} {
		if restored := envelopeTestReasons.Decode(err); restored != nil {
			t.Fatalf("%s decoded as %v", name, restored)
		}
	}
	if rpc.DecodeAppError(status.Error(codes.Aborted, string(apperror.CodeBotNameTaken))) != nil {
		t.Fatal("plain status decoded as an apperror")
	}
}

func TestEnvelopeRoundTripsCatalogCodeAndArgs(t *testing.T) {
	sent := apperror.New(apperror.CodeBotNameTaken, map[string]string{"field": "name"})
	encoded := rpc.AnswerStatus(context.Background(), fmtWrap(sent))
	if encoded == nil {
		t.Fatal("catalog apperror not encoded")
	}
	wire := overWire(t, encoded)
	definition, _ := apperror.Lookup(apperror.CodeBotNameTaken)
	if got := status.Code(wire); got != codes.Aborted {
		t.Fatalf("code = %v, want Aborted for 409", got)
	}
	if got := status.Convert(wire).Message(); got != definition.Detail {
		t.Fatalf("message = %q, want the catalog detail", got)
	}

	restored := rpc.DecodeAppError(wire)
	if restored == nil {
		t.Fatal("envelope not decoded")
	}
	if got := apperror.CodeOf(restored); got != apperror.CodeBotNameTaken {
		t.Fatalf("code = %q", got)
	}
	if got := apperror.ArgsOf(restored); len(got) != 1 || got["field"] != "name" {
		t.Fatalf("args = %v", got)
	}
	if !errors.Is(rpc.Received(restored), wire) || !errors.Is(restored, wire) {
		t.Fatal("received status is not on the chain")
	}
	report := errs.Analyze(context.Background(), restored)
	if !report.Remote || report.Reason != string(apperror.CodeBotNameTaken) {
		t.Fatalf("report = %+v", report)
	}
}

// The envelope carries the fault the server attributes the error to: the
// declared fault of a provider code, and the status class of an undeclared
// one. A client reads it as the remote fault, so a provider failure the
// server logged is a dependency failure on both sides.
func TestEnvelopeCarriesTheServerFault(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sent        error
		wantFault   string
		clientFault apperror.Fault
	}{
		{"declared provider fault", apperror.Wrap(apperror.CodeAgentProviderAuthFailed, errors.New("api error 401"), nil), "dependency", apperror.FaultDependency},
		{"client status", apperror.New(apperror.CodeBotNameTaken, map[string]string{"field": "name"}), "client", apperror.FaultServer},
		{"server status", apperror.Wrap(apperror.CodeWorkspaceUnreachable, errors.New("dial"), nil), "server", apperror.FaultDependency},
		{"server status with a dependency cause", apperror.Wrap(apperror.CodeWorkspaceUnreachable, errs.WrapDependency(errors.New("dial"), "reach workspace"), nil), "dependency", apperror.FaultDependency},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := overWire(t, rpc.AnswerStatus(context.Background(), fmtWrap(tc.sent)))
			if got := errorInfo(t, wire).GetMetadata()[rpc.MetadataFault]; got != tc.wantFault {
				t.Fatalf("envelope fault = %q, want %q", got, tc.wantFault)
			}
			report := errs.Analyze(context.Background(), rpc.DecodeAppError(wire))
			if !report.Remote || string(report.RemoteFault) != tc.wantFault || report.Fault != tc.clientFault {
				t.Fatalf("client report = %+v; want remote_fault=%s fault=%s", report, tc.wantFault, tc.clientFault)
			}
		})
	}
}

// A server that predates the fault key sends the code and args alone. The
// client decodes it as before and attributes it to the dependency.
func TestEnvelopeWithoutFaultFromAnOlderServer(t *testing.T) {
	st, err := status.New(codes.Unauthenticated, "old server").WithDetails(&errdetails.ErrorInfo{
		Reason: string(apperror.CodeAgentProviderAuthFailed),
		Domain: "memoh.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	restored := rpc.DecodeAppError(overWire(t, st.Err()))
	if got := apperror.CodeOf(restored); got != apperror.CodeAgentProviderAuthFailed {
		t.Fatalf("code = %q", got)
	}
	report := errs.Analyze(context.Background(), restored)
	if !report.Remote || report.RemoteFault != "" || report.Fault != apperror.FaultDependency {
		t.Fatalf("report = %+v; want remote dependency without remote_fault", report)
	}
}

// A client that predates the fault key restores the code and the catalog
// args only; the fault is never taken for an arg.
func TestEnvelopeFaultIsNotAnArg(t *testing.T) {
	restored := rpc.DecodeAppError(overWire(t, rpc.AnswerStatus(context.Background(), apperror.New(apperror.CodeBotNameTaken, map[string]string{"field": "name"}))))
	if got := apperror.ArgsOf(restored); len(got) != 1 || got["field"] != "name" {
		t.Fatalf("args = %v, want the catalog args alone", got)
	}
}

func errorInfo(t *testing.T, err error) *errdetails.ErrorInfo {
	t.Helper()
	for _, detail := range status.Convert(err).Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok {
			return info
		}
	}
	t.Fatalf("%v carries no ErrorInfo", err)
	return nil
}

// An error without a catalog error of its own travels as the generic code for
// its fault, and a cancellation by the caller as Canceled.
func TestAnswerStatusAnswersEveryError(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
		code   codes.Code
		fault  string
	}{
		{errors.New("plain"), string(apperror.CodeInternal), codes.Internal, "server"},
		{apperror.New("not.in.catalog", nil), string(apperror.CodeInternal), codes.Internal, "server"},
		{errs.NewDependency("down"), string(apperror.CodeInternal), codes.Internal, "dependency"},
		{status.Error(codes.InvalidArgument, "bad payload"), string(apperror.CodeHTTPBadRequest), codes.InvalidArgument, "client"},
	} {
		got := rpc.AnswerStatus(context.Background(), tc.err)
		info := errorInfo(t, got)
		if status.Code(got) != tc.code || info.GetReason() != tc.reason || info.GetMetadata()[rpc.MetadataFault] != tc.fault {
			t.Fatalf("%v encoded as %v reason=%q fault=%q", tc.err, status.Code(got), info.GetReason(), info.GetMetadata()[rpc.MetadataFault])
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := rpc.AnswerStatus(ctx, fmt.Errorf("call: %w", context.Canceled)); status.Code(got) != codes.Canceled {
		t.Fatalf("canceled call encoded as %v", got)
	}
}

func TestEnvelopeRedactsAdapterMessage(t *testing.T) {
	const secret = "envelope-test-bot-token" //nolint:gosec // test fixture, not a credential
	redact.SetSecrets("envelope-test", secret)
	t.Cleanup(func() { redact.SetSecrets("envelope-test") })

	message := "telegram rejected " + secret + " at https://user:pass@api.example.test/bot?token=abc"
	wire := overWire(t, envelopeTestReasons[0].Status(message))
	if got := status.Convert(wire).Message(); got != envelopeTestReasons[0].Message {
		t.Fatalf("status message = %q, want the fixed message", got)
	}
	restored := envelopeTestReasons.Decode(wire)
	if restored == nil {
		t.Fatal("envelope not decoded")
	}
	text := restored.Error()
	for _, leaked := range []string{secret, "user:pass", "token=abc"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("adapter message %q leaks %q", text, leaked)
		}
	}
	if !strings.Contains(text, "telegram rejected") || !strings.Contains(text, "https://api.example.test/bot") {
		t.Fatalf("adapter message = %q", text)
	}
	if !errors.Is(restored, errEnvelopeFirst) {
		t.Fatalf("restored %v lost the sentinel", restored)
	}
}

func TestReceivedLeavesOtherErrorsUnchanged(t *testing.T) {
	err := errors.New("plain")
	if !errors.Is(rpc.Received(err), err) {
		t.Fatal("Received changed an error Restored did not return")
	}
}

func fmtWrap(err error) error {
	return errors.Join(errors.New("context"), err)
}
