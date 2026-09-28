package grpctransport

// Characterization tests for what a channel process receives across the turn
// gRPC transport when an Agent run fails (scenario 9: a plain error versus a
// catalog error). They pin the encoding each error class gets.

import (
	"context"
	"errors"
	"strings"
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

// A plain error reaches the channel as codes.Internal with a fixed text. A
// catalog apperror crosses with its code; in both cases the raw cause text
// stays on the server.
func TestTransportEncodesPlainAndCatalogErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		wantCode apperror.Code
	}{
		{"plain", errors.New("SECRET provider exploded"), ""},
		{"coded", apperror.Wrap(apperror.CodeAgentProviderOverloaded, errors.New("SECRET 503"), nil), apperror.CodeAgentProviderOverloaded},
	} {
		for path, deliver := range map[string]func(*testing.T, error) error{
			"start": startErrorOverTransport,
			"run":   runErrorOverTransport,
		} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				got := deliver(t, tc.err)
				if strings.Contains(got.Error(), "SECRET") {
					t.Fatalf("cause text leaked: %v", got)
				}
				if code := apperror.CodeOf(got); code != tc.wantCode {
					t.Fatalf("apperror code over transport = %q, want %q", code, tc.wantCode)
				}
				if tc.wantCode != "" {
					return
				}
				st, ok := status.FromError(got)
				if !ok || st.Code() != codes.Internal || st.Message() != "internal turn operation failed" {
					t.Fatalf("error = %v, want Internal \"internal turn operation failed\"", got)
				}
			})
		}
	}
}
