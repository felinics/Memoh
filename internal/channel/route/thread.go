package route

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/singleflight"

	session "github.com/felinics/memoh/internal/chat/thread"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// ThreadStore is the Thread-owned persistence surface needed by Channel route
// orchestration.
type ThreadStore interface {
	Create(context.Context, session.CreateInput) (session.Thread, error)
	Get(context.Context, string) (session.Thread, error)
	SoftDelete(context.Context, string) error
}

// ThreadRouteStore owns the active-thread pointer for an external route.
type ThreadRouteStore interface {
	GetByID(context.Context, string) (Route, error)
	SetActiveThread(context.Context, string, string) error
	// CompareAndSetActiveThread moves the pointer to threadID only while it
	// still holds expectedThreadID ("" for none) and reports whether it did.
	CompareAndSetActiveThread(ctx context.Context, routeID, expectedThreadID, threadID string) (bool, error)
}

// ThreadCoordinator coordinates the Channel-owned route pointer with
// Thread-owned lifecycle operations.
type ThreadCoordinator struct {
	routes  ThreadRouteStore
	threads ThreadStore
	logger  *slog.Logger
	// creating joins the callers that find one route without a usable Thread
	// at the same time, so they create a single Thread between them.
	creating singleflight.Group
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
	if strings.TrimSpace(route.ActiveThreadID) == "" {
		return session.Thread{}, pgx.ErrNoRows
	}
	return c.threads.Get(ctx, route.ActiveThreadID)
}

// CreateNew creates a Thread and best-effort advances the route pointer. The
// historical behavior is preserved: a successful Thread creation is returned
// even when route activation fails.
func (c *ThreadCoordinator) CreateNew(ctx context.Context, input session.CreateInput) (session.Thread, error) {
	if strings.TrimSpace(input.Type) == "" {
		input.Type = session.TypeChat
	}
	thread, err := c.threads.Create(ctx, input)
	if err != nil {
		return session.Thread{}, fmt.Errorf("create new session: %w", err)
	}
	if err := c.routes.SetActiveThread(ctx, input.RouteID, thread.ID); err != nil {
		c.logger.WarnContext(ctx, "failed to set active session on route", slog.Any("error", err))
	}
	return thread, nil
}

// createSessionAttempts bounds how often a creator reads the route again after
// another writer moved the pointer first.
const createSessionAttempts = 3

var errRouteKeptChanging = errors.New("route active session kept changing")

// CreateIfAbsent returns the route's usable active Thread, and creates one from
// input only when the route has none. Messages that reach a new route together
// (a channel's first backlog, a burst of forwards) all land in the Thread this
// returns: callers in one process share a single creation, and the route
// pointer moves only from the value its creator read, so a creator in another
// process that loses discards its Thread and returns the one that won.
func (c *ThreadCoordinator) CreateIfAbsent(ctx context.Context, input session.CreateInput) (session.Thread, error) {
	result := c.creating.DoChan(strings.TrimSpace(input.RouteID), func() (any, error) {
		// Detached, because the callers joined here share it: one of them
		// giving up must not fail it for the rest. Not bounded either: Create
		// runs the SessionStart hook, which can take long on a cold workspace.
		return c.createIfAbsent(context.WithoutCancel(ctx), input)
	})
	select {
	case <-ctx.Done():
		return session.Thread{}, ctx.Err()
	case r := <-result:
		if r.Err != nil {
			return session.Thread{}, r.Err
		}
		return r.Val.(session.Thread), nil
	}
}

func (c *ThreadCoordinator) createIfAbsent(ctx context.Context, input session.CreateInput) (session.Thread, error) {
	if strings.TrimSpace(input.Type) == "" {
		input.Type = session.TypeChat
	}
	var created session.Thread
	discard := func() {
		if created.ID == "" {
			return
		}
		if err := c.threads.SoftDelete(ctx, created.ID); err != nil {
			c.logger.WarnContext(ctx, "discard unselected session failed", slog.String("session_id", created.ID), slog.Any("error", err))
		}
	}
	for range createSessionAttempts {
		route, err := c.routes.GetByID(ctx, input.RouteID)
		if err != nil {
			discard()
			return session.Thread{}, err
		}
		expected := strings.TrimSpace(route.ActiveThreadID)
		if expected != "" {
			thread, err := c.threads.Get(ctx, expected)
			if err == nil {
				discard()
				return thread, nil
			}
			// Only a selected Thread that no longer exists is replaced; any
			// other failure says nothing about whether it is still in use.
			if !errors.Is(err, pgx.ErrNoRows) {
				discard()
				return session.Thread{}, fmt.Errorf("read active session: %w", err)
			}
		}
		if created.ID == "" {
			if created, err = c.threads.Create(ctx, input); err != nil {
				return session.Thread{}, fmt.Errorf("create new session: %w", err)
			}
		}
		set, err := c.routes.CompareAndSetActiveThread(ctx, input.RouteID, expected, created.ID)
		if err != nil {
			err = fmt.Errorf("select new session on route: %w", err)
			// The write may have committed before its reply was lost.
			route, readErr := c.routes.GetByID(ctx, input.RouteID)
			if readErr != nil {
				// Keep the Thread: deleting it might delete the selected one.
				return session.Thread{}, err
			}
			if strings.TrimSpace(route.ActiveThreadID) == created.ID {
				return created, nil
			}
			discard()
			return session.Thread{}, err
		}
		if set {
			return created, nil
		}
		// Another writer moved the pointer since the read: read it again.
	}
	discard()
	return session.Thread{}, errRouteKeptChanging
}

// SetActiveThread updates the Channel-owned active Thread pointer.
func (s *DBService) SetActiveThread(ctx context.Context, routeID, threadID string) error {
	pgRouteID, err := dbpkg.ParseUUID(routeID)
	if err != nil {
		return fmt.Errorf("invalid route id: %w", err)
	}
	var pgThreadID pgtype.UUID
	if strings.TrimSpace(threadID) != "" {
		pgThreadID, err = dbpkg.ParseUUID(threadID)
		if err != nil {
			return fmt.Errorf("invalid session id: %w", err)
		}
	}
	return s.queries.SetRouteActiveSession(ctx, sqlc.SetRouteActiveSessionParams{
		ID:              pgRouteID,
		ActiveSessionID: pgThreadID,
	})
}

// CompareAndSetActiveThread moves the route pointer to threadID only while it
// still holds expectedThreadID ("" for none).
func (s *DBService) CompareAndSetActiveThread(ctx context.Context, routeID, expectedThreadID, threadID string) (bool, error) {
	pgRouteID, err := dbpkg.ParseUUID(routeID)
	if err != nil {
		return false, fmt.Errorf("invalid route id: %w", err)
	}
	pgThreadID, err := dbpkg.ParseUUID(threadID)
	if err != nil {
		return false, fmt.Errorf("invalid session id: %w", err)
	}
	var pgExpected pgtype.UUID
	if strings.TrimSpace(expectedThreadID) != "" {
		pgExpected, err = dbpkg.ParseUUID(expectedThreadID)
		if err != nil {
			return false, fmt.Errorf("invalid expected session id: %w", err)
		}
	}
	rows, err := s.queries.CompareAndSetRouteActiveSession(ctx, sqlc.CompareAndSetRouteActiveSessionParams{
		ID:                pgRouteID,
		ActiveSessionID:   pgThreadID,
		ExpectedSessionID: pgExpected,
	})
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// EnrichThreads projects Channel-owned route metadata onto Thread view
// records. It fetches only routes referenced by the supplied Threads and
// leaves unbound Threads unchanged.
func (s *DBService) EnrichThreads(ctx context.Context, botID string, threads []session.Thread) ([]session.Thread, error) {
	if len(threads) == 0 {
		return []session.Thread{}, nil
	}
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, err
	}

	routeIDs := make([]pgtype.UUID, 0, len(threads))
	threadRouteIDs := make([]pgtype.UUID, len(threads))
	seen := make(map[pgtype.UUID]struct{}, len(threads))
	for i := range threads {
		routeID, parseErr := dbpkg.ParseUUID(threads[i].RouteID)
		if parseErr != nil {
			continue
		}
		threadRouteIDs[i] = routeID
		if _, ok := seen[routeID]; ok {
			continue
		}
		seen[routeID] = struct{}{}
		routeIDs = append(routeIDs, routeID)
	}

	out := append([]session.Thread(nil), threads...)
	if len(routeIDs) == 0 {
		return out, nil
	}

	projections, err := s.queries.ListChatRouteThreadProjectionsByIDs(ctx, sqlc.ListChatRouteThreadProjectionsByIDsParams{
		BotID:    pgBotID,
		RouteIds: routeIDs,
	})
	if err != nil {
		return nil, err
	}
	byID := make(map[pgtype.UUID]sqlc.ListChatRouteThreadProjectionsByIDsRow, len(projections))
	for _, projection := range projections {
		byID[projection.ID] = projection
	}
	for i := range out {
		projection, ok := byID[threadRouteIDs[i]]
		if !ok {
			continue
		}
		out[i].RouteMetadata = parseJSONMap(projection.Metadata)
		out[i].RouteConversationType = dbpkg.TextToString(projection.ConversationType)
	}
	return out, nil
}
