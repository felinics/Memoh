package route

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type coordinatorRouteStore struct {
	route     Route
	getErr    error
	activeID  string
	activeErr error
}

func (s *coordinatorRouteStore) GetByID(context.Context, string) (Route, error) {
	return s.route, s.getErr
}

func (s *coordinatorRouteStore) SetActiveThread(_ context.Context, _ string, threadID string) error {
	s.activeID = threadID
	return s.activeErr
}

func (s *coordinatorRouteStore) CompareAndSetActiveThread(_ context.Context, _, _, threadID string) (bool, error) {
	s.activeID = threadID
	return s.activeErr == nil, s.activeErr
}

type coordinatorThreadStore struct {
	thread      session.Thread
	getID       string
	createInput session.CreateInput
	createErr   error
}

func (s *coordinatorThreadStore) Get(_ context.Context, id string) (session.Thread, error) {
	s.getID = id
	return s.thread, nil
}

func (s *coordinatorThreadStore) Create(_ context.Context, input session.CreateInput) (session.Thread, error) {
	s.createInput = input
	return s.thread, s.createErr
}

func (*coordinatorThreadStore) SoftDelete(context.Context, string) error { return nil }

func TestThreadCoordinatorGetActiveReadsRouteThenThread(t *testing.T) {
	routes := &coordinatorRouteStore{route: Route{ActiveThreadID: "thread-1"}}
	threads := &coordinatorThreadStore{thread: session.Thread{ID: "thread-1"}}
	coordinator := NewThreadCoordinator(nil, routes, threads)

	got, err := coordinator.GetActive(context.Background(), "route-1")
	if err != nil {
		t.Fatalf("GetActive() error = %v", err)
	}
	if got.ID != "thread-1" || threads.getID != "thread-1" {
		t.Fatalf("GetActive() = %#v, get id = %q", got, threads.getID)
	}
}

func TestThreadCoordinatorGetActiveWithoutSelectionReturnsNoRows(t *testing.T) {
	coordinator := NewThreadCoordinator(nil, &coordinatorRouteStore{}, &coordinatorThreadStore{})
	_, err := coordinator.GetActive(context.Background(), "route-1")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetActive() error = %v, want pgx.ErrNoRows", err)
	}
}

func TestThreadCoordinatorCreateNewPreservesBestEffortActivation(t *testing.T) {
	routes := &coordinatorRouteStore{activeErr: errors.New("activation failed")}
	threads := &coordinatorThreadStore{thread: session.Thread{ID: "thread-1"}}
	coordinator := NewThreadCoordinator(nil, routes, threads)

	got, err := coordinator.CreateNew(context.Background(), session.CreateInput{
		BotID:   "bot-1",
		RouteID: "route-1",
	})
	if err != nil {
		t.Fatalf("CreateNew() error = %v", err)
	}
	if got.ID != "thread-1" {
		t.Fatalf("CreateNew() = %#v", got)
	}
	if threads.createInput.Type != session.TypeChat {
		t.Fatalf("created type = %q, want %q", threads.createInput.Type, session.TypeChat)
	}
	if routes.activeID != "thread-1" {
		t.Fatalf("activated thread = %q, want thread-1", routes.activeID)
	}
}

type routeThreadQueries struct {
	dbstore.Queries
	projections []sqlc.ListChatRouteThreadProjectionsByIDsRow
	params      sqlc.ListChatRouteThreadProjectionsByIDsParams
	calls       int
}

func (q *routeThreadQueries) ListChatRouteThreadProjectionsByIDs(_ context.Context, params sqlc.ListChatRouteThreadProjectionsByIDsParams) ([]sqlc.ListChatRouteThreadProjectionsByIDsRow, error) {
	q.params = params
	q.calls++
	return q.projections, nil
}

func TestEnrichThreadsPreservesRouteProjection(t *testing.T) {
	routeID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	botID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	queries := &routeThreadQueries{projections: []sqlc.ListChatRouteThreadProjectionsByIDsRow{{
		ID:               routeID,
		ConversationType: pgtype.Text{String: "group", Valid: true},
		Metadata:         []byte(`{"conversation_name":"Memoh"}`),
	}}}
	service := NewService(nil, queries)

	got, err := service.EnrichThreads(context.Background(), botID.String(), []session.Thread{{
		ID:      "thread-1",
		RouteID: routeID.String(),
	}})
	if err != nil {
		t.Fatalf("EnrichThreads() error = %v", err)
	}
	if len(got) != 1 || got[0].RouteConversationType != "group" {
		t.Fatalf("EnrichThreads() = %#v", got)
	}
	if got[0].RouteMetadata["conversation_name"] != "Memoh" {
		t.Fatalf("RouteMetadata = %#v", got[0].RouteMetadata)
	}
	if queries.calls != 1 || queries.params.BotID != botID {
		t.Fatalf("projection query calls = %d, bot id = %s", queries.calls, queries.params.BotID.String())
	}
	if len(queries.params.RouteIds) != 1 || queries.params.RouteIds[0] != routeID {
		t.Fatalf("projection route ids = %#v", queries.params.RouteIds)
	}
}

func TestEnrichThreadsQueriesOnlyUniqueReferencedRoutes(t *testing.T) {
	firstRouteID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	secondRouteID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	botID := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	queries := &routeThreadQueries{}
	service := NewService(nil, queries)

	threads := []session.Thread{
		{ID: "thread-1", RouteID: firstRouteID.String()},
		{ID: "thread-2", RouteID: firstRouteID.String()},
		{ID: "thread-3", RouteID: secondRouteID.String()},
		{ID: "thread-4"},
		{ID: "thread-5", RouteID: "not-a-uuid"},
	}
	got, err := service.EnrichThreads(context.Background(), botID.String(), threads)
	if err != nil {
		t.Fatalf("EnrichThreads() error = %v", err)
	}
	if len(got) != len(threads) {
		t.Fatalf("EnrichThreads() returned %d threads, want %d", len(got), len(threads))
	}
	if queries.calls != 1 {
		t.Fatalf("projection query calls = %d, want 1", queries.calls)
	}
	if queries.params.BotID != botID {
		t.Fatalf("projection bot id = %s, want %s", queries.params.BotID.String(), botID.String())
	}
	if len(queries.params.RouteIds) != 2 ||
		queries.params.RouteIds[0] != firstRouteID ||
		queries.params.RouteIds[1] != secondRouteID {
		t.Fatalf("projection route ids = %#v", queries.params.RouteIds)
	}
}

func TestEnrichThreadsSkipsProjectionQueryWithoutRouteIDs(t *testing.T) {
	botID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	queries := &routeThreadQueries{}
	service := NewService(nil, queries)
	threads := []session.Thread{{ID: "thread-1"}, {ID: "thread-2", RouteID: "not-a-uuid"}}

	got, err := service.EnrichThreads(context.Background(), botID.String(), threads)
	if err != nil {
		t.Fatalf("EnrichThreads() error = %v", err)
	}
	if queries.calls != 0 {
		t.Fatalf("projection query calls = %d, want 0", queries.calls)
	}
	if len(got) != len(threads) || got[0].ID != "thread-1" || got[1].ID != "thread-2" {
		t.Fatalf("EnrichThreads() = %#v", got)
	}
}

// raceStore is one route and its threads as the database holds them, shared by
// every caller; it serves as both the route and the thread store.
type raceStore struct {
	mu       sync.Mutex
	activeID string
	movedTo  string // when set, another process selects it right after the first route read
	failGet  string // when set, reading this thread fails with a transient error
	lostAck  bool   // when set, the conditional write commits and then reports an error
	threads  map[string]session.Thread
	created  int
	deleted  []string
	entered  chan struct{} // when set, closed as the first Create starts
	gate     chan struct{} // when set, Create waits for it to close
	once     sync.Once
}

func (s *raceStore) GetByID(context.Context, string) (Route, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	route := Route{ActiveThreadID: s.activeID}
	if s.movedTo != "" {
		s.activeID, s.movedTo = s.movedTo, ""
	}
	return route, nil
}

func (*raceStore) SetActiveThread(context.Context, string, string) error { return nil }

func (s *raceStore) CompareAndSetActiveThread(_ context.Context, _, expected, threadID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeID != expected {
		return false, nil
	}
	s.activeID = threadID
	if s.lostAck {
		return false, errors.New("connection reset")
	}
	return true, nil
}

func (s *raceStore) Get(_ context.Context, id string) (session.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == s.failGet {
		return session.Thread{}, errors.New("connection reset")
	}
	if thread, ok := s.threads[id]; ok {
		return thread, nil
	}
	return session.Thread{}, pgx.ErrNoRows
}

func (s *raceStore) Create(context.Context, session.CreateInput) (session.Thread, error) {
	if s.gate != nil {
		s.once.Do(func() { close(s.entered) })
		<-s.gate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created++
	thread := session.Thread{ID: fmt.Sprintf("thread-%d", s.created)}
	s.threads[thread.ID] = thread
	return thread, nil
}

func (s *raceStore) SoftDelete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, id)
	return nil
}

func TestThreadCoordinatorCreateIfAbsentGivesConcurrentCallersOneThread(t *testing.T) {
	store := &raceStore{threads: map[string]session.Thread{}, entered: make(chan struct{}), gate: make(chan struct{})}
	coordinator := NewThreadCoordinator(nil, store, store)
	call := func() {
		thread, err := coordinator.CreateIfAbsent(context.Background(), session.CreateInput{RouteID: "route-1"})
		if err != nil || thread.ID != "thread-1" {
			t.Errorf("CreateIfAbsent = %q, %v; want every caller on thread-1", thread.ID, err)
		}
	}
	// The first caller holds its creation open while 19 more arrive.
	var arrived, done sync.WaitGroup
	done.Add(20)
	go func() { defer done.Done(); call() }()
	<-store.entered
	arrived.Add(19)
	for i := 0; i < 19; i++ {
		go func() { defer done.Done(); arrived.Done(); call() }()
	}
	arrived.Wait()
	// The arrivals were counted just before their calls; yielding lets them
	// reach the shared creation. A caller scheduled after the gate opens finds
	// the selected thread and still returns thread-1, so this can only weaken
	// how surely the test catches a missing join, never fail a correct one.
	for range 100 {
		runtime.Gosched()
	}
	close(store.gate)
	done.Wait()
	if store.created != 1 || store.activeID != "thread-1" {
		t.Fatalf("created %d threads, route points at %q; want one thread selected", store.created, store.activeID)
	}
}

func TestThreadCoordinatorCreateIfAbsentYieldsToAnotherProcess(t *testing.T) {
	// The route points at a deleted thread, so this caller creates one; another
	// process selects its own thread before this caller writes.
	store := &raceStore{activeID: "thread-deleted", movedTo: "thread-other", threads: map[string]session.Thread{"thread-other": {ID: "thread-other"}}}
	got, err := NewThreadCoordinator(nil, store, store).CreateIfAbsent(context.Background(), session.CreateInput{RouteID: "route-1"})
	if err != nil || got.ID != "thread-other" || store.activeID != "thread-other" {
		t.Fatalf("CreateIfAbsent = %q, %v, route points at %q; want the other process's thread kept", got.ID, err, store.activeID)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "thread-1" {
		t.Fatalf("deleted %v, want the thread this caller created", store.deleted)
	}
}

func TestThreadCoordinatorCreateIfAbsentRetriesWhenTheWinnerVanished(t *testing.T) {
	// Another process selects a thread right after this caller's read, and that
	// thread is gone by the time this caller reads the route again.
	store := &raceStore{movedTo: "thread-gone", threads: map[string]session.Thread{}}
	got, err := NewThreadCoordinator(nil, store, store).CreateIfAbsent(context.Background(), session.CreateInput{RouteID: "route-1"})
	if err != nil || got.ID != "thread-1" || store.activeID != "thread-1" {
		t.Fatalf("CreateIfAbsent = %q, %v, route points at %q; want this caller's thread selected", got.ID, err, store.activeID)
	}
	if store.created != 1 || len(store.deleted) != 0 {
		t.Fatalf("created %d, deleted %v; want one thread and nothing discarded", store.created, store.deleted)
	}
}

func TestThreadCoordinatorCreateIfAbsentKeepsSelectionWhenItCannotBeRead(t *testing.T) {
	store := &raceStore{activeID: "thread-live", failGet: "thread-live", threads: map[string]session.Thread{}}
	if _, err := NewThreadCoordinator(nil, store, store).CreateIfAbsent(context.Background(), session.CreateInput{RouteID: "route-1"}); err == nil {
		t.Fatal("CreateIfAbsent succeeded although the selected thread could not be read")
	}
	if store.created != 0 || store.activeID != "thread-live" {
		t.Fatalf("created %d, route points at %q; want the selection left alone", store.created, store.activeID)
	}
}

func TestThreadCoordinatorCreateIfAbsentKeepsThreadWhoseSelectionReplyWasLost(t *testing.T) {
	store := &raceStore{lostAck: true, threads: map[string]session.Thread{}}
	got, err := NewThreadCoordinator(nil, store, store).CreateIfAbsent(context.Background(), session.CreateInput{RouteID: "route-1"})
	if err != nil || got.ID != "thread-1" || len(store.deleted) != 0 {
		t.Fatalf("CreateIfAbsent = %q, %v, deleted %v; want the selected thread returned and kept", got.ID, err, store.deleted)
	}
}

func TestThreadCoordinatorCreateIfAbsentReturnsWhenTheCallerGivesUp(t *testing.T) {
	store := &raceStore{threads: map[string]session.Thread{}, entered: make(chan struct{}), gate: make(chan struct{})}
	coordinator := NewThreadCoordinator(nil, store, store)
	done := make(chan struct{})
	go func() { // holds the shared creation open
		defer close(done)
		_, _ = coordinator.CreateIfAbsent(context.Background(), session.CreateInput{RouteID: "route-1"})
	}()
	<-store.entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gaveUp := make(chan error, 1)
	go func() {
		_, err := coordinator.CreateIfAbsent(ctx, session.CreateInput{RouteID: "route-1"})
		gaveUp <- err
	}()
	select {
	case err := <-gaveUp:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("CreateIfAbsent with a cancelled context = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("CreateIfAbsent kept waiting for the shared creation after its caller gave up")
	}
	close(store.gate)
	<-done
}
