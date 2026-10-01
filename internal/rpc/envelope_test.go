package rpc_test

import (
	"context"
	"errors"
	"strings"
	"testing"

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
		})
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
	encoded := rpc.AppErrorStatus(fmtWrap(sent))
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

func TestAppErrorStatusRejectsNonCatalogErrors(t *testing.T) {
	for _, err := range []error{errors.New("plain"), apperror.New("not.in.catalog", nil)} {
		if rpc.AppErrorStatus(err) != nil {
			t.Fatalf("%v encoded", err)
		}
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
