package route

import (
	"context"
	"errors"
	"testing"
)

type routeStoreFake struct {
	findRoute      Route
	findErr        error
	createRoute    Route
	createErr      error
	routes         []Route
	replyTarget    string
	metadata       map[string]any
	touchedBotID   string
	ensuredBotID   string
	activeThreadID string

	projectionCalls    int
	projectionBotID    string
	projectionRouteIDs []string
}

func (s *routeStoreFake) CreateRoute(context.Context, CreateInput) (Route, error) {
	return s.createRoute, s.createErr
}

func (s *routeStoreFake) FindRoute(context.Context, string, string, string, string) (Route, error) {
	return s.findRoute, s.findErr
}

func (s *routeStoreFake) FindRouteByID(_ context.Context, routeID string) (Route, error) {
	for _, item := range s.routes {
		if item.ID == routeID {
			return item, nil
		}
	}
	return Route{}, ErrNotFound
}

func (s *routeStoreFake) ListRoutes(context.Context, string) ([]Route, error) {
	return s.routes, nil
}

func (s *routeStoreFake) ListRouteThreadProjections(_ context.Context, botID string, routeIDs []string) ([]ThreadProjection, error) {
	s.projectionCalls++
	s.projectionBotID = botID
	s.projectionRouteIDs = append([]string(nil), routeIDs...)
	out := make([]ThreadProjection, 0, len(routeIDs))
	for _, id := range routeIDs {
		for _, r := range s.routes {
			if r.ID == id {
				out = append(out, ThreadProjection{
					RouteID:          r.ID,
					ConversationType: r.ConversationType,
					Metadata:         r.Metadata,
				})
			}
		}
	}
	return out, nil
}

func (*routeStoreFake) DeleteRoute(context.Context, string) error { return nil }

func (s *routeStoreFake) SetReplyTarget(_ context.Context, _ string, target string) error {
	s.replyTarget = target
	return nil
}

func (s *routeStoreFake) SetMetadata(_ context.Context, _ string, metadata map[string]any) error {
	s.metadata = metadata
	return nil
}

func (s *routeStoreFake) SetActiveThread(_ context.Context, _ string, threadID string) error {
	s.activeThreadID = threadID
	return nil
}

func (s *routeStoreFake) TouchBot(_ context.Context, botID string) error {
	s.touchedBotID = botID
	return nil
}

func (s *routeStoreFake) EnsureBot(_ context.Context, botID string) error {
	s.ensuredBotID = botID
	return nil
}

func TestResolveConversationUpdatesExistingRoute(t *testing.T) {
	store := &routeStoreFake{findRoute: Route{
		ID:          "route-1",
		BotID:       "bot-1",
		ReplyTarget: "old",
		Metadata:    map[string]any{"existing": true},
	}}
	service := NewService(nil, store)

	result, err := service.ResolveConversation(t.Context(), ResolveInput{
		BotID:       "bot-1",
		ReplyTarget: "new",
		Metadata:    map[string]any{"incoming": true},
	})
	if err != nil {
		t.Fatalf("ResolveConversation() error = %v", err)
	}
	if result.Created || result.RouteID != "route-1" {
		t.Fatalf("result = %#v", result)
	}
	if store.replyTarget != "new" || store.touchedBotID != "bot-1" {
		t.Fatalf("store updates = %#v", store)
	}
	if store.metadata["existing"] != true || store.metadata["incoming"] != true {
		t.Fatalf("metadata = %#v", store.metadata)
	}
}

func TestResolveConversationCreatesMissingRoute(t *testing.T) {
	store := &routeStoreFake{
		findErr:     ErrNotFound,
		createRoute: Route{ID: "route-1", BotID: "bot-1"},
	}
	service := NewService(nil, store)

	result, err := service.ResolveConversation(t.Context(), ResolveInput{BotID: "bot-1"})
	if err != nil {
		t.Fatalf("ResolveConversation() error = %v", err)
	}
	if !result.Created || store.ensuredBotID != "bot-1" {
		t.Fatalf("result = %#v, ensured bot = %q", result, store.ensuredBotID)
	}
}

func TestResolveConversationRejectsUnexpectedFindError(t *testing.T) {
	service := NewService(nil, &routeStoreFake{findErr: errors.New("read failed")})

	_, err := service.ResolveConversation(t.Context(), ResolveInput{BotID: "bot-1"})
	if err == nil {
		t.Fatal("expected error")
	}
}
