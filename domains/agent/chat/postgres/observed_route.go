package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/agent/chat/message"
	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type observedRouteQueries interface {
	ListObservedRoutes(context.Context, agentsqlc.ListObservedRoutesParams) ([]agentsqlc.ListObservedRoutesRow, error)
}

// ObservedRouteReader adapts Agent-owned SQL to the Message observation port.
type ObservedRouteReader struct {
	queries observedRouteQueries
}

func NewObservedRouteReader(database agentsqlc.DBTX) *ObservedRouteReader {
	return newObservedRouteReader(agentsqlc.New(database))
}

func newObservedRouteReader(queries observedRouteQueries) *ObservedRouteReader {
	return &ObservedRouteReader{queries: queries}
}

func (r *ObservedRouteReader) ListObservedRoutes(ctx context.Context, query message.ObservedRouteQuery) ([]message.ObservedRoute, error) {
	botID, err := db.ParseUUID(query.BotID)
	if err != nil {
		return nil, err
	}
	identityID, err := optionalUUID(query.ChannelIdentityID)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.ListObservedRoutes(ctx, agentsqlc.ListObservedRoutesParams{
		BotID:             botID,
		ChannelIdentityID: identityID,
	})
	if err != nil {
		return nil, err
	}
	items := make([]message.ObservedRoute, 0, len(rows))
	for _, row := range rows {
		items = append(items, observedRoute(row.RouteID, row.LastObservedAt))
	}
	return items, nil
}

func observedRoute(routeID pgtype.UUID, lastObservedAt pgtype.Timestamptz) message.ObservedRoute {
	return message.ObservedRoute{
		RouteID:        uuidString(routeID),
		LastObservedAt: timestamp(lastObservedAt),
	}
}

var _ message.ObservedRouteReader = (*ObservedRouteReader)(nil)
