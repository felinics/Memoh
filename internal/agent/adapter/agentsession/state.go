package agentsession

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/runtime/agentstate"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// RuntimeStateStore reads runtime configuration generations.
type RuntimeStateStore struct {
	queries dbstore.Queries
}

func NewRuntimeStateStore(queries dbstore.Queries) *RuntimeStateStore {
	return &RuntimeStateStore{queries: queries}
}

func (s *RuntimeStateStore) RuntimeConfigEpoch(
	ctx context.Context,
	botID string,
	sessionID string,
) (agentstate.RuntimeConfigEpoch, error) {
	pgBotID, err := dbpkg.ParseUUID(strings.TrimSpace(botID))
	if err != nil {
		return agentstate.RuntimeConfigEpoch{}, fmt.Errorf("invalid runtime bot id: %w", err)
	}
	var pgSessionID pgtype.UUID
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		pgSessionID, err = dbpkg.ParseUUID(sessionID)
		if err != nil {
			return agentstate.RuntimeConfigEpoch{}, fmt.Errorf("invalid agent session id: %w", err)
		}
	}
	row, err := s.queries.GetRuntimeConfigEpoch(ctx, sqlc.GetRuntimeConfigEpochParams{
		BotID: pgBotID, SessionID: pgSessionID,
	})
	if err != nil {
		return agentstate.RuntimeConfigEpoch{}, fmt.Errorf("load runtime config epoch: %w", err)
	}
	return agentstate.RuntimeConfigEpoch{
		Bot: row.BotRuntimeConfigEpoch, Session: row.SessionRuntimeConfigEpoch,
	}, nil
}
