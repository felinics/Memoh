package grpctransport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/rpc"
)

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
