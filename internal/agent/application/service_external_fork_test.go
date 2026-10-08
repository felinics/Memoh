package application

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const (
	forkTestBotID     = "00000000-0000-0000-0000-000000000701"
	forkTestSessionID = "00000000-0000-0000-0000-000000000702"
	forkTestTurnID    = "00000000-0000-0000-0000-000000000703"
)

type forkAnchorQueries struct {
	dbstore.Queries
}

func (forkAnchorQueries) GetTurnAgentTurnID(context.Context, sqlc.GetTurnAgentTurnIDParams) (string, error) {
	return "runtime-turn-1", nil
}

type failingForkDriver struct {
	external.Driver
	err error
}

func (d failingForkDriver) ForkThread(context.Context, string, string, map[string]any, string) (map[string]any, error) {
	return nil, d.err
}

// A runtime failure while forking reaches the session handler with its public
// code; other errors are left for the handler to answer.
func TestPrepareExternalForkAnswersRuntimeFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code apperror.Code
	}{
		{"runtime unavailable", external.Unavailable(errors.New("session has no codex thread to fork")), apperror.CodeExternalRuntimeUnavailable},
		{"auth required", external.Fail(external.FailureAuthRequired, external.ErrAuthRequired), apperror.CodeExternalRuntimeAuthRequired},
		{"missing dependency", &external.DependencyMissingError{DependencyID: "codex"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{
				queries: forkAnchorQueries{},
				sessionService: &fakeBackgroundSessionService{getFn: func(context.Context, string) (session.Thread, error) {
					return session.Thread{ID: forkTestSessionID, BotID: forkTestBotID, BotAgentID: "agent-1", RuntimeType: session.RuntimeCodex}, nil
				}},
				externalDrivers: map[string]external.Driver{session.RuntimeCodex: failingForkDriver{err: tc.err}},
			}
			_, err := svc.PrepareExternalFork(context.Background(), forkTestBotID, forkTestSessionID, forkTestTurnID)
			if got := apperror.CodeOf(err); got != tc.code {
				t.Fatalf("code = %q, want %q: %v", got, tc.code, err)
			}
			if !errors.Is(apperror.CauseOf(err), tc.err) && !errors.Is(err, tc.err) {
				t.Fatalf("error lost the runtime failure: %v", err)
			}
		})
	}
}
