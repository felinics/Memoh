package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/redact"
	"github.com/felinics/memoh/internal/rpc"
	"github.com/felinics/memoh/internal/rpc/runtimepb"
)

// legacyDecode is the decoding of a client built before the envelope: it reads
// only the status code and, for Unknown, the status message.
func legacyDecode(err error) error {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return errors.Join(ErrUnavailable, err)
	case codes.Unauthenticated:
		return errors.Join(ErrUnavailable, ErrUnauthenticated, err)
	case codes.Unknown:
		return errors.New(status.Convert(err).Message())
	default:
		return err
	}
}

func rawCallOverWire(t *testing.T, handlerErr error) error {
	t.Helper()
	_, err := runtimepb.NewRuntimeServiceClient(dialOverWire(t, handlerErr)).Call(context.Background(), &runtimepb.CallRequest{Method: "m"})
	return err
}

func TestServerCatalogErrorRoundTrip(t *testing.T) {
	sent := fmt.Errorf("rename: %w", apperror.Wrap(apperror.CodeBotNameTaken, errors.New("SECRET duplicate key"), map[string]string{"field": "name"}))
	err := callOverWire(t, sent)
	if apperror.CodeOf(err) != apperror.CodeBotNameTaken || apperror.ArgsOf(err)["field"] != "name" {
		t.Fatalf("got %v (args %v), want the catalog error", err, apperror.ArgsOf(err))
	}
	if strings.Contains(status.Convert(rpc.Received(err)).Message(), "SECRET") {
		t.Fatalf("cause text leaked: %v", rpc.Received(err))
	}
}

func TestServerRedactsPublicAdapterMessage(t *testing.T) {
	const secret = "runtime-encode-bot-token" //nolint:gosec // test fixture, not a credential
	redact.SetSecrets("runtime-encode", secret)
	t.Cleanup(func() { redact.SetSecrets("runtime-encode") })

	err := callOverWire(t, Public(errors.New("telegram rejected "+secret+" at https://user:pass@api.example.test/bot")))
	if !errors.Is(err, errPublic) {
		t.Fatalf("%v lost the public identity", err)
	}
	for _, leaked := range []string{secret, "user:pass"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("adapter message %q leaks %q", err.Error(), leaked)
		}
	}
	if !strings.Contains(err.Error(), "telegram rejected") {
		t.Fatalf("adapter message %q lost its text", err.Error())
	}
}

// A client built before the envelope reads the fixed status message and the
// status code only.
func TestLegacyClientReadsServerEncoding(t *testing.T) {
	t.Run("public error loses its adapter text", func(t *testing.T) {
		err := legacyDecode(rawCallOverWire(t, Public(errors.New("telegram: chat not found"))))
		if err.Error() != publicReason.Message {
			t.Fatalf("legacy text = %q, want the fixed message", err.Error())
		}
	})
	t.Run("503 catalog error reads as a link outage", func(t *testing.T) {
		err := legacyDecode(rawCallOverWire(t, apperror.New(apperror.CodeQueueAdmissionUnavailable, nil)))
		if !errors.Is(err, ErrUnavailable) || apperror.CodeOf(err) != "" {
			t.Fatalf("legacy error = %v, want ErrUnavailable without a code", err)
		}
	})
	t.Run("other catalog errors arrive as a raw status", func(t *testing.T) {
		err := legacyDecode(rawCallOverWire(t, apperror.New(apperror.CodeBotNameTaken, nil)))
		if status.Code(err) != codes.Aborted || apperror.CodeOf(err) != "" {
			t.Fatalf("legacy error = %v, want a raw Aborted status", err)
		}
	})
}
