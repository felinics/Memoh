package runtime

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/redact"
	"github.com/felinics/memoh/internal/rpc"
)

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
