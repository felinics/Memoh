package agentsession

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/runtime/agentstate"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// RuntimeStateStore reads runtime generations and guards workspace configuration.
type RuntimeStateStore struct {
	queries dbstore.Queries
}

type stateTransactionRunner interface {
	InTx(context.Context, func(dbstore.Queries) error) error
}

type stateTransactionCapability interface {
	SupportsTransactions() bool
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

// GuardRuntimeSync serializes an old runtime process's workspace reads/writes
// against bot-scoped reset publication. The database guard deliberately wraps
// the external callback: once this transaction owns the bot row lock, a reset
// successor cannot acquire or publish until the callback has finished.
func (s *RuntimeStateStore) GuardRuntimeSync(
	ctx context.Context,
	botID string,
	expectedBotEpoch int64,
	fn func(context.Context) error,
) error {
	pgBotID, err := dbpkg.ParseUUID(strings.TrimSpace(botID))
	if err != nil {
		return fmt.Errorf("invalid runtime bot id: %w", err)
	}
	txer, ok := s.queries.(stateTransactionRunner)
	capability, supported := s.queries.(stateTransactionCapability)
	if !ok || !supported || !capability.SupportsTransactions() {
		return errors.New("runtime sync guard requires transaction support")
	}
	return txer.InTx(ctx, func(queries dbstore.Queries) error {
		if _, err := queries.LockBotForRuntimeReset(ctx, pgBotID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return agentstate.ErrRuntimeConfigStale
			}
			return fmt.Errorf("lock runtime configuration: %w", err)
		}
		if _, err := queries.GetBotRuntimeReset(ctx, pgBotID); err == nil {
			return agentstate.ErrRuntimeConfigResetInProgress
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check bot runtime reset: %w", err)
		}
		epoch, err := queries.GetRuntimeConfigEpoch(ctx, sqlc.GetRuntimeConfigEpochParams{
			BotID: pgBotID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return agentstate.ErrRuntimeConfigStale
		}
		if err != nil {
			return fmt.Errorf("load guarded runtime config epoch: %w", err)
		}
		if epoch.BotRuntimeConfigEpoch != expectedBotEpoch {
			return fmt.Errorf(
				"%w: expected bot epoch %d, found %d",
				agentstate.ErrRuntimeConfigStale,
				expectedBotEpoch,
				epoch.BotRuntimeConfigEpoch,
			)
		}
		return fn(ctx)
	})
}

// Head reads the last committed round. History clear removes the head so a
// warm process cannot continue from its old in-memory conversation.
func (s *RuntimeStateStore) Head(ctx context.Context, botID, sessionID string) (agentstate.SessionPublicationHead, bool, error) {
	pgBotID, pgSessionID, err := parseScope(botID, sessionID)
	if err != nil {
		return agentstate.SessionPublicationHead{}, false, err
	}
	runID, err := s.queries.GetAgentSessionPublicationHead(ctx, sqlc.GetAgentSessionPublicationHeadParams{BotID: pgBotID, SessionID: pgSessionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return agentstate.SessionPublicationHead{}, false, nil
	}
	if err != nil {
		return agentstate.SessionPublicationHead{}, false, fmt.Errorf("load agent session publication head: %w", err)
	}
	return agentstate.SessionPublicationHead{RunID: runID.String()}, true, nil
}

func parseScope(botID, sessionID string) (pgBotID, pgSessionID pgtype.UUID, err error) {
	pgBotID, err = dbpkg.ParseUUID(strings.TrimSpace(botID))
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("invalid agent runtime bot id: %w", err)
	}
	pgSessionID, err = dbpkg.ParseUUID(strings.TrimSpace(sessionID))
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("invalid agent runtime session id: %w", err)
	}
	return pgBotID, pgSessionID, nil
}
