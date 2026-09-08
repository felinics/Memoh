package application

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/context/trajectory"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/runtimefence"
)

type trajectoryScopeQueries struct {
	session     sqlc.BotSession
	reads       int
	writes      []sqlc.AppendContextTrajectoryEventParams
	err         error
	panicOnRead bool
}

func (*trajectoryScopeQueries) SupportsTransactions() bool { return true }

func (q *trajectoryScopeQueries) GetSessionByID(context.Context, pgtype.UUID) (sqlc.BotSession, error) {
	q.reads++
	if q.panicOnRead {
		panic("binding unavailable")
	}
	return q.session, q.err
}

func (q *trajectoryScopeQueries) AppendContextTrajectoryEvent(_ context.Context, params sqlc.AppendContextTrajectoryEventParams) (int64, error) {
	q.writes = append(q.writes, params)
	if params.RuntimeFencingToken != q.session.RuntimeFencingToken {
		return 0, pgx.ErrNoRows
	}
	return params.Sequence, nil
}

func TestContextTrajectoryFreezesUnmanagedSessionBeforeCapture(t *testing.T) {
	botID, err := db.ParseUUID(pipelineTestBotID)
	if err != nil {
		t.Fatal(err)
	}
	queries := &trajectoryScopeQueries{session: sqlc.BotSession{BotID: botID}}
	sink := newContextTrajectorySink(t.Context(), queries, botID, pipelineTestSessionID)
	queries.session.RuntimeFencingToken = 12
	recorder := trajectory.NewRecorder(sink)
	recorder.Bind("00000000-0000-0000-0000-000000000123", pipelineTestSessionID)
	recorder.Record(t.Context(), "wire_request", nil, trajectory.Block{Content: "old input"})
	if queries.reads != 1 || len(queries.writes) != 1 || queries.writes[0].RuntimeFencingToken != 0 || recorder.Stats().Errors != 1 {
		t.Fatalf("capture rebound to successor: reads=%d writes=%#v stats=%#v", queries.reads, queries.writes, recorder.Stats())
	}
}

func TestContextTrajectoryUsesExactManagedScope(t *testing.T) {
	botID, err := db.ParseUUID(pipelineTestBotID)
	if err != nil {
		t.Fatal(err)
	}
	fence := runtimefence.Fence{BotID: pipelineTestBotID, SessionID: pipelineTestSessionID, Token: 7}
	ctx := runtimefence.WithContext(t.Context(), fence)
	queries := &trajectoryScopeQueries{session: sqlc.BotSession{BotID: botID, RuntimeFencingToken: 7}}
	sink := newContextTrajectorySink(ctx, queries, botID, pipelineTestSessionID)
	event := trajectory.Event{
		RunID: "00000000-0000-0000-0000-000000000123", SessionID: pipelineTestSessionID,
		CaptureID: "00000000-0000-0000-0000-000000000124", Sequence: 1,
	}
	if err := sink.Append(ctx, event, nil); err != nil || queries.reads != 0 || len(queries.writes) != 1 || queries.writes[0].RuntimeFencingToken != 7 {
		t.Fatalf("managed capture = %v, reads=%d, writes=%#v", err, queries.reads, queries.writes)
	}
	fence.Token++
	if err := sink.Append(runtimefence.WithContext(ctx, fence), event, nil); !errors.Is(err, runtimefence.ErrStale) || len(queries.writes) != 1 {
		t.Fatalf("reused capture accepted successor token: %v", err)
	}
	childSession := "00000000-0000-0000-0000-000000000125"
	child := newContextTrajectorySink(ctx, queries, botID, childSession)
	event.SessionID = childSession
	if err := child.Append(ctx, event, nil); !errors.Is(err, runtimefence.ErrStale) || queries.reads != 0 || len(queries.writes) != 1 {
		t.Fatalf("child capture inherited parent ownership: %v", err)
	}
}

func TestContextTrajectoryReportsBindingFailureWithoutRetryingOwnership(t *testing.T) {
	botID, err := db.ParseUUID(pipelineTestBotID)
	if err != nil {
		t.Fatal(err)
	}
	queries := &trajectoryScopeQueries{err: pgx.ErrNoRows}
	sink := newContextTrajectorySink(t.Context(), queries, botID, pipelineTestSessionID)
	queries.err = nil
	if err := sink.Append(t.Context(), trajectory.Event{}, nil); !errors.Is(err, pgx.ErrNoRows) || queries.reads != 1 || len(queries.writes) != 0 {
		t.Fatalf("unbound capture recovered under a different owner: %v", err)
	}
	queries.panicOnRead = true
	sink = newContextTrajectorySink(t.Context(), queries, botID, pipelineTestSessionID)
	if err := sink.Append(t.Context(), trajectory.Event{}, nil); err == nil || len(queries.writes) != 0 {
		t.Fatal("binding failure escaped the capture boundary")
	}
}
