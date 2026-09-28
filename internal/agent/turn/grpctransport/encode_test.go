package grpctransport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/rpc"
)

// legacyMapClientError is the decoding of a client built before the envelope:
// it maps the status code alone to a turn sentinel.
func legacyMapClientError(err error) error {
	switch status.Code(err) {
	case codes.Aborted:
		return turn.ErrSessionBusy
	case codes.AlreadyExists:
		return turn.ErrDuplicateTurn
	case codes.ResourceExhausted:
		if status.Convert(err).Message() == turnDeferredStatusMessage {
			return turn.ErrTurnDeferred
		}
		return err
	case codes.PermissionDenied:
		return turn.ErrTeamNotServed
	default:
		return err
	}
}

// legacyRuntimeControlClientError adds the prefix check the pre-envelope
// runtime control client ran before legacyMapClientError.
func legacyRuntimeControlClientError(err error) error {
	st := status.Convert(err)
	if code, ok := strings.CutPrefix(st.Message(), runtimeControlErrorPrefix); ok && st.Code() == codes.FailedPrecondition {
		return apperror.New(apperror.Code(code), nil)
	}
	return legacyMapClientError(err)
}

// asReceived returns err as a client receives it.
func asReceived(err error) error {
	return status.FromProto(status.Convert(err).Proto()).Err()
}

type failingControls struct {
	scriptedService
	err error
}

func (f *failingControls) RuntimeCommands(context.Context, turn.RuntimeControlRequest) ([]turn.RuntimeCommand, error) {
	return nil, f.err
}

func (f *failingControls) RuntimeControls(context.Context, turn.RuntimeControlRequest) (turn.RuntimeControls, error) {
	return turn.RuntimeControls{}, f.err
}

func (f *failingControls) SetRuntimeMode(context.Context, turn.RuntimeControlRequest) (turn.RuntimeModeState, error) {
	return turn.RuntimeModeState{}, f.err
}

func (f *failingControls) ExecuteRuntimeCommand(context.Context, turn.RuntimeControlRequest) (turn.RuntimeCommandResult, error) {
	return turn.RuntimeCommandResult{}, f.err
}

func TestServerSendsTurnSentinelsAsEnvelope(t *testing.T) {
	for _, entry := range turnReasons {
		wire := asReceived((*Server)(nil).mapError(context.Background(), "test", entry.Err))
		if reason, ok := rpc.ReasonOf(wire); !ok || reason != entry.Reason {
			t.Fatalf("%s: server sent %v without the envelope reason", entry.Reason, wire)
		}
	}
}

func TestServerCatalogErrorRoundTrip(t *testing.T) {
	sent := fmt.Errorf("rename: %w", apperror.Wrap(apperror.CodeBotNameTaken, errors.New("SECRET duplicate key"), map[string]string{"field": "name"}))
	client, cleanup := newTestClient(t, &failingControls{scriptedService: scriptedService{startErr: sent}, err: sent}, "secret")
	t.Cleanup(cleanup)

	_, startErr := client.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "team-1"})
	_, controlErr := client.RuntimeCommands(context.Background(), turn.RuntimeControlRequest{TeamID: "team-1"})
	for name, err := range map[string]error{"start": startErr, "runtime control": controlErr} {
		if apperror.CodeOf(err) != apperror.CodeBotNameTaken || apperror.ArgsOf(err)["field"] != "name" {
			t.Fatalf("%s: got %v (args %v), want the catalog error", name, err, apperror.ArgsOf(err))
		}
		if strings.Contains(err.Error(), "SECRET") || strings.Contains(status.Convert(err).Message(), "SECRET") {
			t.Fatalf("%s: cause text leaked: %v", name, err)
		}
		if errors.Is(err, turn.ErrSessionBusy) {
			t.Fatalf("%s: catalog conflict read as a busy session", name)
		}
	}
}

// A client built before the envelope reads the turn sentinels as before, but
// maps catalog errors by status code alone.
func TestLegacyClientReadsServerEncoding(t *testing.T) {
	ctx := context.Background()
	for _, entry := range turnReasons {
		if got := legacyMapClientError(asReceived((*Server)(nil).mapError(ctx, "test", entry.Err))); !errors.Is(got, entry.Err) {
			t.Fatalf("%s: legacy client read %v", entry.Reason, got)
		}
	}
	for _, tc := range []struct {
		name string
		code apperror.Code
		want error
	}{
		{"409 reads as a busy session", apperror.CodeBotNameTaken, turn.ErrSessionBusy},
		{"403 reads as a team not served", apperror.CodeRuntimeControlForbidden, turn.ErrTeamNotServed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent := apperror.New(tc.code, nil)
			for name, got := range map[string]error{
				"turn":            legacyMapClientError(asReceived((*Server)(nil).mapError(ctx, "test", sent))),
				"runtime control": legacyRuntimeControlClientError(asReceived((*Server)(nil).mapError(ctx, "runtime control", sent))),
			} {
				if !errors.Is(got, tc.want) || apperror.CodeOf(got) != "" {
					t.Fatalf("%s: legacy client read %v, want %v", name, got, tc.want)
				}
			}
		})
	}
	t.Run("503 arrives as a raw status", func(t *testing.T) {
		got := legacyMapClientError(asReceived((*Server)(nil).mapError(ctx, "test", apperror.New(apperror.CodeAgentProviderOverloaded, nil))))
		if status.Code(got) != codes.Unavailable || apperror.CodeOf(got) != "" {
			t.Fatalf("legacy client read %v, want a raw Unavailable status", got)
		}
	})
}
