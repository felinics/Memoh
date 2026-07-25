package route

import (
	"context"
	"errors"
	"testing"

	session "github.com/memohai/memoh/domains/agent/chat/thread"
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
	if !errors.Is(err, ErrActiveThreadNotFound) {
		t.Fatalf("GetActive() error = %v, want ErrActiveThreadNotFound", err)
	}
}

func TestThreadCoordinatorCreateNewPreservesBestEffortActivation(t *testing.T) {
	routes := &coordinatorRouteStore{
		route: Route{
			ID:               "route-1",
			ConversationType: "group",
			ReplyTarget:      "chat-1",
			Metadata:         map[string]any{"conversation_name": " Memoh "},
		},
		activeErr: errors.New("activation failed"),
	}
	threads := &coordinatorThreadStore{thread: session.Thread{ID: "thread-1"}}
	coordinator := NewThreadCoordinator(nil, routes, threads)

	got, err := coordinator.CreateNew(context.Background(), session.CreateInput{
		BotID:            "bot-1",
		RouteID:          "route-1",
		ConversationType: "explicit",
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
	if threads.createInput.ConversationType != "explicit" ||
		threads.createInput.ConversationName != "Memoh" ||
		threads.createInput.ReplyTarget != "chat-1" {
		t.Fatalf("created route snapshot = %#v", threads.createInput)
	}
	if routes.activeID != "thread-1" {
		t.Fatalf("activated thread = %q, want thread-1", routes.activeID)
	}
}

func TestThreadCoordinatorEnsureActiveCreatesWithRouteSnapshot(t *testing.T) {
	routes := &coordinatorRouteStore{route: Route{
		ID:               "route-1",
		ConversationType: "thread",
		ReplyTarget:      "topic-1",
		Metadata:         map[string]any{"conversation_name": "Topic"},
	}}
	threads := &coordinatorThreadStore{thread: session.Thread{ID: "thread-1"}}

	_, err := NewThreadCoordinator(nil, routes, threads).
		EnsureActive(context.Background(), "bot-1", "route-1", "telegram")
	if err != nil {
		t.Fatalf("EnsureActive() error = %v", err)
	}
	if got := threads.createInput; got.ConversationType != "thread" ||
		got.ConversationName != "Topic" ||
		got.ReplyTarget != "topic-1" {
		t.Fatalf("created route snapshot = %#v", got)
	}
}

func TestEnrichThreadsPreservesRouteProjection(t *testing.T) {
	service := NewService(nil, &routeStoreFake{routes: []Route{{
		ID:               "route-1",
		BotID:            "bot-1",
		Platform:         "telegram",
		ConversationType: "group",
		Metadata:         map[string]any{"conversation_name": "Memoh"},
	}}})

	got, err := service.EnrichThreads(context.Background(), "bot-1", []session.Thread{{
		ID:      "thread-1",
		RouteID: "route-1",
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
}
