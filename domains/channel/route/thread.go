package route

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	session "github.com/memohai/memoh/domains/agent/chat/thread"
)

// ThreadStore is the Thread-owned persistence surface needed by Channel route
// orchestration.
type ThreadStore interface {
	Create(context.Context, session.CreateInput) (session.Thread, error)
	Get(context.Context, string) (session.Thread, error)
}

// ThreadRouteStore owns the active-thread pointer for an external route.
type ThreadRouteStore interface {
	GetByID(context.Context, string) (Route, error)
	SetActiveThread(context.Context, string, string) error
}

// ThreadCoordinator coordinates the Channel-owned route pointer with
// Thread-owned lifecycle operations.
type ThreadCoordinator struct {
	routes  ThreadRouteStore
	threads ThreadStore
	logger  *slog.Logger
}

func NewThreadCoordinator(log *slog.Logger, routes ThreadRouteStore, threads ThreadStore) *ThreadCoordinator {
	if log == nil {
		log = slog.Default()
	}
	return &ThreadCoordinator{
		routes:  routes,
		threads: threads,
		logger:  log.With(slog.String("service", "channel/route/thread")),
	}
}

// GetActive returns the active Thread selected by the route.
func (c *ThreadCoordinator) GetActive(ctx context.Context, routeID string) (session.Thread, error) {
	route, err := c.routes.GetByID(ctx, routeID)
	if err != nil {
		return session.Thread{}, err
	}
	return c.activeThread(ctx, route)
}

func (c *ThreadCoordinator) activeThread(ctx context.Context, route Route) (session.Thread, error) {
	if strings.TrimSpace(route.ActiveThreadID) == "" {
		return session.Thread{}, ErrActiveThreadNotFound
	}
	return c.threads.Get(ctx, route.ActiveThreadID)
}

// CreateNew creates a Thread and best-effort advances the route pointer. The
// historical behavior is preserved: a successful Thread creation is returned
// even when route activation fails.
func (c *ThreadCoordinator) CreateNew(ctx context.Context, input session.CreateInput) (session.Thread, error) {
	route, err := c.routes.GetByID(ctx, input.RouteID)
	if err != nil {
		return session.Thread{}, fmt.Errorf("get route snapshot: %w", err)
	}
	return c.createNew(ctx, route, input)
}

func (c *ThreadCoordinator) createNew(ctx context.Context, route Route, input session.CreateInput) (session.Thread, error) {
	if strings.TrimSpace(input.Type) == "" {
		input.Type = session.TypeChat
	}
	input.RouteID = route.ID
	if strings.TrimSpace(input.ConversationType) == "" {
		input.ConversationType = route.ConversationType
	}
	if strings.TrimSpace(input.ConversationName) == "" {
		input.ConversationName = route.ConversationName()
	}
	if strings.TrimSpace(input.ReplyTarget) == "" {
		input.ReplyTarget = route.ReplyTarget
	}
	thread, err := c.threads.Create(ctx, input)
	if err != nil {
		return session.Thread{}, fmt.Errorf("create new session: %w", err)
	}
	if err := c.routes.SetActiveThread(ctx, route.ID, thread.ID); err != nil {
		c.logger.WarnContext(ctx, "failed to set active session on route", slog.Any("error", err))
	}
	return thread, nil
}

// EnsureActive returns the selected Thread, or creates one when the route
// currently has no usable active Thread.
func (c *ThreadCoordinator) EnsureActive(ctx context.Context, botID, routeID, channelType string) (session.Thread, error) {
	route, err := c.routes.GetByID(ctx, routeID)
	if err != nil {
		return session.Thread{}, err
	}
	if strings.TrimSpace(route.ActiveThreadID) != "" {
		thread, err := c.activeThread(ctx, route)
		if err == nil {
			return thread, nil
		}
	}
	thread, err := c.createNew(ctx, route, session.CreateInput{
		BotID:       botID,
		RouteID:     routeID,
		ChannelType: channelType,
	})
	if err != nil {
		return session.Thread{}, fmt.Errorf("auto-create session: %w", err)
	}
	return thread, nil
}

// SetActiveThread updates the Channel-owned active Thread pointer.
func (s *Manager) SetActiveThread(ctx context.Context, routeID, threadID string) error {
	return s.store.SetActiveThread(ctx, routeID, threadID)
}

// EnrichThreads projects Channel-owned route metadata onto Thread view
// records. It queries only the routes this page actually references — a bot can
// own far more routes than one listing shows — and skips the query entirely
// when no Thread is route-bound. Unbound Threads are left unchanged.
func (s *Manager) EnrichThreads(ctx context.Context, botID string, threads []session.Thread) ([]session.Thread, error) {
	if len(threads) == 0 {
		return []session.Thread{}, nil
	}
	seen := make(map[string]struct{}, len(threads))
	routeIDs := make([]string, 0, len(threads))
	for _, thread := range threads {
		id := strings.TrimSpace(thread.RouteID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		routeIDs = append(routeIDs, id)
	}
	if len(routeIDs) == 0 {
		return append([]session.Thread(nil), threads...), nil
	}
	projections, err := s.store.ListRouteThreadProjections(ctx, botID, routeIDs)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]ThreadProjection, len(projections))
	for _, projection := range projections {
		byID[projection.RouteID] = projection
	}
	out := append([]session.Thread(nil), threads...)
	for i := range out {
		route, ok := byID[out[i].RouteID]
		if !ok {
			continue
		}
		out[i].RouteMetadata = route.Metadata
		out[i].RouteConversationType = route.ConversationType
	}
	return out, nil
}
