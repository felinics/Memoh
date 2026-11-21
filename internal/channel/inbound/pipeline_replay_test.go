package inbound

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const replayTestSessionID = "66666666-6666-6666-6666-666666666666"

// replayQueries is a session with no persisted events. Each replay counts
// itself at its first query and waits there until release is closed or its
// context ends.
type replayQueries struct {
	dbstore.Queries
	started atomic.Int32
	release chan struct{}
	err     error
}

func (q *replayQueries) GetActiveHistoryCheckpoint(ctx context.Context, _ pgtype.UUID) (sqlc.BotHistoryCheckpoint, error) {
	q.started.Add(1)
	select {
	case <-q.release:
	case <-ctx.Done():
		return sqlc.BotHistoryCheckpoint{}, ctx.Err()
	}
	if q.err != nil {
		return sqlc.BotHistoryCheckpoint{}, q.err
	}
	return sqlc.BotHistoryCheckpoint{}, pgx.ErrNoRows
}

func (*replayQueries) ListSessionEventsBySessionPageBeforeWithinBytes(context.Context, sqlc.ListSessionEventsBySessionPageBeforeWithinBytesParams) ([]sqlc.ListSessionEventsBySessionPageBeforeWithinBytesRow, error) {
	return nil, nil
}

func (*replayQueries) CountSessionEvents(context.Context, pgtype.UUID) (int64, error) {
	return 0, nil
}

func (*replayQueries) NextSessionEventCursor(context.Context) (int64, error) {
	return 1, nil
}

func (*replayQueries) CreateSessionEvent(context.Context, sqlc.CreateSessionEventParams) (pgtype.UUID, error) {
	return pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, nil
}

func newReplayProcessor(queries *replayQueries) (*ChannelInboundProcessor, *timeline.Pipeline) {
	pipeline := timeline.NewPipeline(timeline.RenderParams{})
	processor := &ChannelInboundProcessor{}
	processor.SetPipeline(pipeline, timeline.NewEventStore(slog.New(slog.DiscardHandler), queries), nil)
	return processor, pipeline
}

// Messages that reach a cold session together load its history once, and
// later ones find it loaded even when that history is empty: another replay
// would replace whatever was pushed since the first.
func TestReplayPipelineSessionLoadsAColdSessionOnce(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		queries := &replayQueries{release: make(chan struct{})}
		processor, pipeline := newReplayProcessor(queries)

		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				processor.replayPipelineSession(context.Background(), "bot-1", replayTestSessionID)
			})
		}
		// Every caller now waits in the shared replay or on it. Read the count
		// before releasing, so a failure does not leave the bubble deadlocked.
		synctest.Wait()
		concurrent := queries.started.Load()
		close(queries.release)
		wg.Wait()
		processor.replayPipelineSession(context.Background(), "bot-1", replayTestSessionID)

		if concurrent != 1 {
			t.Fatalf("concurrent replays started = %d, want 1", concurrent)
		}
		if got := queries.started.Load(); got != 1 || !pipeline.HasSession(replayTestSessionID) {
			t.Fatalf("after the replay: replays = %d, loaded = %v; want 1, true", got, pipeline.HasSession(replayTestSessionID))
		}
	})
}

// The replay serves every message waiting on it, so the message that started
// it giving up does not cost the others the session's history.
func TestReplayPipelineSessionOutlivesTheCallerThatStartedIt(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		queries := &replayQueries{release: make(chan struct{})}
		processor, pipeline := newReplayProcessor(queries)

		first, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Go(func() { processor.replayPipelineSession(first, "bot-1", replayTestSessionID) })
		synctest.Wait()
		wg.Go(func() { processor.replayPipelineSession(context.Background(), "bot-1", replayTestSessionID) })
		synctest.Wait()
		cancel()
		synctest.Wait()
		close(queries.release)
		wg.Wait()

		if got := queries.started.Load(); got != 1 || !pipeline.HasSession(replayTestSessionID) {
			t.Fatalf("replays = %d, loaded = %v; want 1, true", got, pipeline.HasSession(replayTestSessionID))
		}
	})
}

// A message whose session failed to load still gets its context rendered, but
// the session stays cold, so the next message retries the replay instead of
// building on a session resident without its history; so does a session
// evicted after it loaded.
func TestProjectInboundEventRetriesASessionThatFailedToLoad(t *testing.T) {
	t.Parallel()
	queries := &replayQueries{release: make(chan struct{}), err: errors.New("database unavailable")}
	close(queries.release)
	processor, pipeline := newReplayProcessor(queries)
	project := func() timeline.RenderedContext {
		event := inboundTestEvent()
		event.SessionID = replayTestSessionID
		_, rc := processor.projectInboundEvent(context.Background(), "bot-1", replayTestSessionID, event)
		return rc
	}

	if rc := project(); len(rc) == 0 || pipeline.HasSession(replayTestSessionID) {
		t.Fatalf("after a failed replay: rendered %d, loaded %v; want the message rendered, session cold", len(rc), pipeline.HasSession(replayTestSessionID))
	}
	queries.err = nil
	project()
	pipeline.DropSession(replayTestSessionID)
	project()

	if got := queries.started.Load(); got != 3 || !pipeline.HasSession(replayTestSessionID) {
		t.Fatalf("replays = %d, loaded = %v; want 3, true", got, pipeline.HasSession(replayTestSessionID))
	}
}
