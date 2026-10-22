package contextepoch

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// fakeSessionQueries mirrors the SQL-side monotonic guard: a write whose
// epoch does not strictly increase, or whose anchor moves backward, affects
// no row and reports "not found".
type fakeSessionQueries struct {
	mu     sync.Mutex
	states map[string]string
	writes []State
}

func (f *fakeSessionQueries) GetSessionContextEpoch(_ context.Context, sessionID pgtype.UUID) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[sessionID.String()], nil
}

func (f *fakeSessionQueries) AdvanceSessionContextEpoch(_ context.Context, arg sqlc.AdvanceSessionContextEpochParams) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := arg.SessionID.String()
	current, err := decodeState(f.states[key])
	if err != nil {
		return "", err
	}
	next, err := decodeState(string(arg.State))
	if err != nil {
		return "", err
	}
	if current.Epoch >= next.Epoch || current.AnchorCreatedAtMs > next.AnchorCreatedAtMs {
		return "", pgx.ErrNoRows
	}
	f.states[key] = string(arg.State)
	f.writes = append(f.writes, next)
	return string(arg.State), nil
}

const epochTestSessionID = "33333333-3333-4333-8333-333333333333"

func newFakeStore(now time.Time) (*Store, *fakeSessionQueries) {
	queries := &fakeSessionQueries{states: map[string]string{}}
	store := NewStore(queries, nil)
	store.SetClock(func() time.Time { return now })
	return store, queries
}

func epochTestMessage(id string, at time.Time) messagepkg.Message {
	return messagepkg.Message{ID: id, Role: "user", CreatedAt: at}
}

func TestLoadWindowInitializesStateFromBeginning(t *testing.T) {
	t.Parallel()
	store, queries := newFakeStore(time.Now())
	base := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	loaded := []messagepkg.Message{epochTestMessage("m1", base), epochTestMessage("m2", base.Add(time.Hour))}
	var gotSince time.Time

	msgs, err := store.LoadWindow(context.Background(), epochTestSessionID, "test",
		func(context.Context, time.Time) (int64, error) { return int64(len(loaded)), nil },
		func(_ context.Context, since time.Time) ([]messagepkg.Message, error) {
			gotSince = since
			return loaded, nil
		})
	if err != nil {
		t.Fatalf("LoadWindow() error = %v", err)
	}
	if len(msgs) != len(loaded) {
		t.Fatalf("loaded %d messages, want %d", len(msgs), len(loaded))
	}
	if !gotSince.Equal(Beginning) {
		t.Fatalf("first load must start at the beginning anchor, got since=%v", gotSince)
	}
	state, err := store.Load(context.Background(), epochTestSessionID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if state.Epoch != 1 || state.LastReason != ReasonInit || state.AnchorCreatedAtMs != 0 {
		t.Fatalf("state = %+v, want epoch 1 init at the zero anchor", state)
	}
	// Init write only: the budget did not bind, so no advance was recorded.
	if len(queries.writes) != 1 {
		t.Fatalf("writes = %d, want exactly the init write", len(queries.writes))
	}
}

func TestLoadWindowRecordsBudgetAdvance(t *testing.T) {
	t.Parallel()
	store, queries := newFakeStore(time.Now())
	base := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	// The session holds three rows since the anchor, but the byte budget
	// admits only the newest two.
	all := []messagepkg.Message{
		epochTestMessage("m1", base),
		epochTestMessage("m2", base.Add(time.Hour)),
		epochTestMessage("m3", base.Add(2*time.Hour)),
	}
	admitted := all[1:]

	msgs, err := store.LoadWindow(context.Background(), epochTestSessionID, "test",
		func(context.Context, time.Time) (int64, error) { return int64(len(all)), nil },
		func(context.Context, time.Time) ([]messagepkg.Message, error) { return admitted, nil })
	if err != nil {
		t.Fatalf("LoadWindow() error = %v", err)
	}
	if len(msgs) != len(admitted) {
		t.Fatalf("loaded %d messages, want %d", len(msgs), len(admitted))
	}
	state, err := store.Load(context.Background(), epochTestSessionID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	wantAnchor := admitted[0].CreatedAt.UnixMilli()
	if state.Epoch != 2 || state.LastReason != ReasonBudget ||
		state.AnchorCreatedAtMs != wantAnchor || state.AnchorMessageID != admitted[0].ID {
		t.Fatalf("state = %+v, want epoch 2 budget advance anchored at %s (%d)", state, admitted[0].ID, wantAnchor)
	}
	if len(queries.writes) != 2 {
		t.Fatalf("writes = %d, want init + budget advance", len(queries.writes))
	}

	// The next load starts at the recorded anchor and measures exactly what
	// it loads, so no further advance fires.
	var gotSince time.Time
	if _, err := store.LoadWindow(context.Background(), epochTestSessionID, "test",
		func(context.Context, time.Time) (int64, error) { return int64(len(admitted)), nil },
		func(_ context.Context, since time.Time) ([]messagepkg.Message, error) {
			gotSince = since
			return admitted, nil
		}); err != nil {
		t.Fatalf("second LoadWindow() error = %v", err)
	}
	if gotSince.UnixMilli() != wantAnchor {
		t.Fatalf("second load since = %v, want recorded anchor %d", gotSince, wantAnchor)
	}
	if len(queries.writes) != 2 {
		t.Fatalf("writes = %d, want no additional advance once the anchor matches the budget", len(queries.writes))
	}
}

func TestAdvanceRejectsBackwardAndStaleMoves(t *testing.T) {
	t.Parallel()
	store, _ := newFakeStore(time.Now())
	ctx := context.Background()
	state := store.ensureInitialized(ctx, epochTestSessionID, "test")

	base := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	forward, err := state.AdvancedBy(base, "m1", ReasonBudget, time.Now())
	if err != nil {
		t.Fatalf("AdvancedBy() error = %v", err)
	}
	if forward.Epoch != state.Epoch+1 {
		t.Fatalf("AdvancedBy epoch = %d, want %d", forward.Epoch, state.Epoch+1)
	}
	if _, err := forward.AdvancedBy(base.Add(-time.Hour), "m0", ReasonBudget, time.Now()); err == nil {
		t.Fatal("moving the anchor backward must be rejected")
	}
	if _, err := state.AdvancedBy(base, "m1", "", time.Now()); err == nil {
		t.Fatal("an advance without a reason must be rejected")
	}

	persisted, err := store.Advance(ctx, epochTestSessionID, forward)
	if err != nil {
		t.Fatalf("Advance() error = %v", err)
	}
	if persisted.Epoch != forward.Epoch {
		t.Fatalf("persisted epoch = %d, want %d", persisted.Epoch, forward.Epoch)
	}
	// A racing write with the older state is a stale no-op, not a regression.
	if _, err := store.Advance(ctx, epochTestSessionID, state); err == nil {
		t.Fatal("re-writing the superseded state must report a stale advance")
	}
	current, err := store.Load(ctx, epochTestSessionID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if current.Epoch != forward.Epoch {
		t.Fatalf("stored epoch = %d after stale write, want %d", current.Epoch, forward.Epoch)
	}
}

func TestNilStoreDegradesToBeginningAnchor(t *testing.T) {
	t.Parallel()
	var store *Store
	loaded := []messagepkg.Message{epochTestMessage("m1", time.Now())}
	var gotSince time.Time
	msgs, err := store.LoadWindow(context.Background(), epochTestSessionID, "test",
		func(context.Context, time.Time) (int64, error) { return 1, nil },
		func(_ context.Context, since time.Time) ([]messagepkg.Message, error) {
			gotSince = since
			return loaded, nil
		})
	if err != nil || len(msgs) != 1 {
		t.Fatalf("LoadWindow() = (%v, %v), want the load to run", msgs, err)
	}
	if !gotSince.Equal(Beginning) {
		t.Fatalf("nil store since = %v, want the beginning anchor", gotSince)
	}
}

func TestAdvanceRejectsSameEpochRace(t *testing.T) {
	t.Parallel()
	store, _ := newFakeStore(time.Now())
	ctx := context.Background()
	state := store.ensureInitialized(ctx, epochTestSessionID, "test")

	base := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	// Two writers both compute epoch N+1 from the same stored state N but
	// pick different anchors; the loser must observe a stale advance instead
	// of silently re-anchoring epoch N+1.
	writerA, err := state.AdvancedBy(base, "m1", ReasonBudget, time.Now())
	if err != nil {
		t.Fatalf("AdvancedBy() error = %v", err)
	}
	writerB, err := state.AdvancedBy(base.Add(time.Hour), "m2", ReasonBudget, time.Now())
	if err != nil {
		t.Fatalf("AdvancedBy() error = %v", err)
	}
	if _, err := store.Advance(ctx, epochTestSessionID, writerA); err != nil {
		t.Fatalf("first Advance() error = %v", err)
	}
	current, err := store.Advance(ctx, epochTestSessionID, writerB)
	if !errors.Is(err, ErrAdvanceStale) {
		t.Fatalf("second Advance() error = %v, want ErrAdvanceStale", err)
	}
	if current.AnchorMessageID != "m1" {
		t.Fatalf("losing writer resolved to %+v, want the winner's state (anchor m1)", current)
	}
}

func TestLoadWindowSkipsNoOpBudgetAdvance(t *testing.T) {
	t.Parallel()
	store, queries := newFakeStore(time.Now())
	ctx := context.Background()
	state := store.ensureInitialized(ctx, epochTestSessionID, "test")

	// Batch-persisted rows share one database now(); a budget cut inside such
	// a batch admits the oldest row whose timestamp already equals the stored
	// anchor (modulo millisecond persistence). The stored anchor names m2,
	// the oldest row the previous budget admitted.
	anchor := time.Date(2026, 10, 20, 8, 0, 0, 123456789, time.UTC)
	advanced, err := state.AdvancedBy(anchor, "m2", ReasonBudget, time.Now())
	if err != nil {
		t.Fatalf("AdvancedBy() error = %v", err)
	}
	if _, err := store.Advance(ctx, epochTestSessionID, advanced); err != nil {
		t.Fatalf("Advance() error = %v", err)
	}
	writesBefore := len(queries.writes)

	// The load still measures more rows than it admitted (siblings share the
	// anchor timestamp), but the oldest admitted row sits at the anchor, so
	// no advance may be recorded.
	loaded := []messagepkg.Message{
		epochTestMessage("m2", anchor),
		epochTestMessage("m3", anchor),
	}
	msgs, err := store.LoadWindow(ctx, epochTestSessionID, "test",
		func(context.Context, time.Time) (int64, error) { return int64(len(loaded) + 1), nil },
		func(context.Context, time.Time) ([]messagepkg.Message, error) { return loaded, nil })
	if err != nil {
		t.Fatalf("LoadWindow() error = %v", err)
	}
	if len(msgs) != len(loaded) {
		t.Fatalf("loaded %d messages, want %d", len(msgs), len(loaded))
	}
	if len(queries.writes) != writesBefore {
		t.Fatalf("writes = %d, want no no-op advance at the same anchor", len(queries.writes))
	}
	state2, err := store.Load(ctx, epochTestSessionID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if state2.Epoch != advanced.Epoch {
		t.Fatalf("epoch = %d, want %d (no no-op advance)", state2.Epoch, advanced.Epoch)
	}

	// A REAL slide at the same millisecond — the oldest admitted message is
	// now a different row of the shared-timestamp batch — must still be
	// booked: the boundary changed even though the anchor millisecond did not.
	loaded2 := []messagepkg.Message{
		epochTestMessage("m3", anchor),
		epochTestMessage("m4", anchor),
	}
	if _, err := store.LoadWindow(ctx, epochTestSessionID, "test",
		func(context.Context, time.Time) (int64, error) { return int64(len(loaded2) + 1), nil },
		func(context.Context, time.Time) ([]messagepkg.Message, error) { return loaded2, nil }); err != nil {
		t.Fatalf("second LoadWindow() error = %v", err)
	}
	state3, err := store.Load(ctx, epochTestSessionID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if state3.Epoch != advanced.Epoch+1 || state3.AnchorMessageID != "m3" ||
		state3.AnchorCreatedAtMs != anchor.UnixMilli() {
		t.Fatalf("state = %+v, want epoch %d re-anchored at m3 (same ms)", state3, advanced.Epoch+1)
	}
}

func TestAdvanceInPlaceRetriesOnStale(t *testing.T) {
	t.Parallel()
	store, queries := newFakeStore(time.Now())
	_ = store
	ctx := context.Background()

	// A concurrent writer holds epoch 5; the first load of this process fails
	// transiently, degrading ensureInitialized to the initial state. The
	// budget-recovery booking must land on top of the stored state, not be
	// reported as booked while nothing was written.
	winner := State{Epoch: 5, AnchorCreatedAtMs: 1000, AnchorMessageID: "m1", LastReason: ReasonBudget}
	raw, err := winner.Encode()
	if err != nil {
		t.Fatalf("encode winner: %v", err)
	}
	queries.states[epochTestSessionID] = string(raw)
	flaky := &flakyEpochQueries{fakeSessionQueries: queries, failLoads: 1}
	var logs bytes.Buffer
	flakyStore := NewStore(flaky, slog.New(slog.NewTextHandler(&logs, nil)))
	flakyStore.SetClock(time.Now)

	state, err := flakyStore.AdvanceInPlace(ctx, epochTestSessionID, "test", ReasonBudgetRecovery)
	if err != nil {
		t.Fatalf("AdvanceInPlace() error = %v", err)
	}
	if state.Epoch != 6 || state.LastReason != ReasonBudgetRecovery ||
		state.AnchorCreatedAtMs != 1000 || state.AnchorMessageID != "m1" {
		t.Fatalf("state = %+v, want the booking retried on top of epoch 5 with its anchor", state)
	}
	// The audit log must describe the committed transition 5→6, not the
	// degraded read the first attempt started from.
	if !strings.Contains(logs.String(), "from_epoch=5") || !strings.Contains(logs.String(), "to_epoch=6") {
		t.Fatalf("audit log = %q, want from_epoch=5 to_epoch=6", logs.String())
	}
}

type flakyEpochQueries struct {
	*fakeSessionQueries
	failLoads int
}

func (f *flakyEpochQueries) GetSessionContextEpoch(ctx context.Context, sessionID pgtype.UUID) (string, error) {
	if f.failLoads > 0 {
		f.failLoads--
		return "", errors.New("transient connection failure")
	}
	return f.fakeSessionQueries.GetSessionContextEpoch(ctx, sessionID)
}

func TestAdvanceInPlaceRecordsBudgetRecovery(t *testing.T) {
	t.Parallel()
	store, _ := newFakeStore(time.Now())
	ctx := context.Background()

	state, err := store.AdvanceInPlace(ctx, epochTestSessionID, "test", ReasonBudgetRecovery)
	if err != nil {
		t.Fatalf("AdvanceInPlace() error = %v", err)
	}
	if state.Epoch != 2 || state.LastReason != ReasonBudgetRecovery {
		t.Fatalf("state = %+v, want epoch 2 with reason budget-recovery", state)
	}
	if state.AnchorCreatedAtMs != 0 {
		t.Fatalf("anchor moved to %d, want the initial zero anchor", state.AnchorCreatedAtMs)
	}

	// A second in-place advance increments the epoch again without moving the
	// anchor; a missing reason is rejected.
	state, err = store.AdvanceInPlace(ctx, epochTestSessionID, "test", ReasonBudgetRecovery)
	if err != nil {
		t.Fatalf("second AdvanceInPlace() error = %v", err)
	}
	if state.Epoch != 3 {
		t.Fatalf("epoch = %d, want 3", state.Epoch)
	}
	if _, err := store.AdvanceInPlace(ctx, epochTestSessionID, "test", ""); err == nil {
		t.Fatal("an in-place advance without a reason must be rejected")
	}
}
