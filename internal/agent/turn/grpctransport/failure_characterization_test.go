package grpctransport

// Characterization tests for what a channel process receives across the turn
// gRPC transport when an Agent run fails (scenario 9: a plain error versus the
// original feedback error). They pin CURRENT behavior; values that look wrong
// are asserted as they are today and marked "current behavior".

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
)

func runErrorOverTransport(t *testing.T, runErr error) error {
	t.Helper()
	events := make(chan turn.Event)
	close(events)
	errs := make(chan error, 1)
	errs <- runErr
	close(errs)
	client, cleanup := newTestClient(t, &scriptedService{handle: &scriptedHandle{events: events, errs: errs}}, "secret")
	t.Cleanup(cleanup)
	handle, err := client.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "team-1"})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	for range handle.Events() {
	}
	var got error
	for err := range handle.Errs() {
		got = err
	}
	return got
}

func startErrorOverTransport(t *testing.T, startErr error) error {
	t.Helper()
	client, cleanup := newTestClient(t, &scriptedService{startErr: startErr}, "secret")
	t.Cleanup(cleanup)
	_, err := client.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "team-1"})
	return err
}

// Current behavior: any error that is neither a transport sentinel nor an
// agentfeedback error reaches the channel as codes.Internal with a fixed text.
// The raw cause does not leak, and an apperror code does not survive either.
func TestCharacterizeTransportFlattensNonFeedbackErrors_CurrentBehavior(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"plain", errors.New("SECRET provider exploded")},
		{"coded", apperror.Wrap(apperror.CodeAgentProviderOverloaded, errors.New("SECRET 503"), nil)},
	} {
		for path, deliver := range map[string]func(*testing.T, error) error{
			"start": startErrorOverTransport,
			"run":   runErrorOverTransport,
		} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				got := deliver(t, tc.err)
				st, ok := status.FromError(got)
				if !ok || st.Code() != codes.Internal || st.Message() != "internal turn operation failed" {
					t.Fatalf("error = %v, want Internal \"internal turn operation failed\"", got)
				}
				if code := apperror.CodeOf(got); code != "" {
					t.Fatalf("apperror code over transport = %q, current behavior has none", code)
				}
			})
		}
	}
}
