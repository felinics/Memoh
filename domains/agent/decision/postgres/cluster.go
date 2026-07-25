// Package postgres implements Decision-owned PostgreSQL persistence.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/agent/chat/runtimefence"
	"github.com/memohai/memoh/domains/agent/decision/approval"
	"github.com/memohai/memoh/domains/agent/decision/input"
	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

// BotSessionWriteLocker locks the API-owned bot row (FOR KEY SHARE) before Agent
// session/decision writes. Composition must bind the locker to the same
// PostgreSQL transaction as the Agent-owner SQLC queries.
//
// Owner: API (api.bots). Agent must not query that table directly.
type BotSessionWriteLocker interface {
	LockBotForSessionWrite(context.Context, pgtype.UUID) (pgtype.UUID, error)
}

// BotSessionWriteLockerFromTx binds a BotSessionWriteLocker to an open transaction.
type BotSessionWriteLockerFromTx func(pgx.Tx) BotSessionWriteLocker

// ChannelIdentity is the minimal inbound identity projection required for
// decision persistence existence checks.
//
// Satisfied by application.ChannelIdentityReader adapters; defined locally so
// this package does not import application (avoids acp↔application cycles).
type ChannelIdentity struct {
	ID          string
	DisplayName string
}

// ChannelIdentityReader looks up Channel-owned inbound identities.
//
// Owner: Channel. Agent must not query channel identity tables directly.
type ChannelIdentityReader interface {
	GetByID(context.Context, string) (ChannelIdentity, error)
}

// Cluster is the PostgreSQL-backed decision persistence factory.
type Cluster struct {
	pool       *pgxpool.Pool
	queries    *agentsqlc.Queries
	newLock    BotSessionWriteLockerFromTx
	identities ChannelIdentityReader
}

// New constructs decision postgres adapters from Agent-owner SQLC plus the
// required cross-owner ports (bot lock factory and channel identity reader).
func New(
	pool *pgxpool.Pool,
	newLock BotSessionWriteLockerFromTx,
	identities ChannelIdentityReader,
) (*Cluster, error) {
	if pool == nil {
		return nil, errors.New("decision postgres adapter requires a pgx pool")
	}
	if newLock == nil {
		return nil, errors.New("decision postgres adapter requires a bot session lock factory")
	}
	if identities == nil {
		return nil, errors.New("decision postgres adapter requires a channel identity reader")
	}
	return &Cluster{
		pool:       pool,
		queries:    agentsqlc.New(pool),
		newLock:    newLock,
		identities: identities,
	}, nil
}

func (c *Cluster) Approval() approval.Persistence {
	return &approvalStore{pool: c.pool, queries: c.queries, newLock: c.newLock, identities: c.identities}
}

func (c *Cluster) Input() input.Persistence {
	return &inputStore{pool: c.pool, queries: c.queries, newLock: c.newLock, identities: c.identities}
}

func (c *Cluster) RuntimeFence() runtimefence.Persistence {
	return &fenceStore{pool: c.pool, queries: c.queries, newLock: c.newLock}
}

type approvalStore struct {
	pool       *pgxpool.Pool
	queries    *agentsqlc.Queries
	newLock    BotSessionWriteLockerFromTx
	lock       BotSessionWriteLocker
	identities ChannelIdentityReader
}

type inputStore struct {
	pool       *pgxpool.Pool
	queries    *agentsqlc.Queries
	newLock    BotSessionWriteLockerFromTx
	lock       BotSessionWriteLocker
	identities ChannelIdentityReader
}

type fenceStore struct {
	pool    *pgxpool.Pool
	queries *agentsqlc.Queries
	newLock BotSessionWriteLockerFromTx
	lock    BotSessionWriteLocker
}

func inTransaction(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	if pool == nil {
		return runtimefence.ErrTransactionsUnsupported
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func parseUUID(value string) (pgtype.UUID, error) {
	return db.ParseUUID(value)
}

func optionalUUID(value string) pgtype.UUID {
	return db.ParseUUIDOrEmpty(value)
}

func optionalInt64(value *int64) pgtype.Int8 {
	if value == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *value, Valid: true}
}

func optionalTime(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func int64Pointer(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func timePointer(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func mapApprovalError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return approval.ErrNotFound
	}
	return err
}

func mapInputError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return input.ErrNotFound
	}
	return err
}

func mapFenceError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return runtimefence.ErrRecordNotFound
	}
	return err
}

func lockBot(ctx context.Context, lock BotSessionWriteLocker, botID string) error {
	if lock == nil {
		return errors.New("bot session locker is required")
	}
	id, err := parseUUID(botID)
	if err != nil {
		return fmt.Errorf("invalid bot id: %w", err)
	}
	if _, err := lock.LockBotForSessionWrite(ctx, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return runtimefence.ErrStale
		}
		return err
	}
	return nil
}

func lockDecisionSequence(ctx context.Context, queries *agentsqlc.Queries, botID, sessionID string) error {
	bot, err := parseUUID(botID)
	if err != nil {
		return err
	}
	session, err := parseUUID(sessionID)
	if err != nil {
		return err
	}
	_, err = queries.LockSessionDecisionSequence(ctx, agentsqlc.LockSessionDecisionSequenceParams{
		BotID: bot, SessionID: session,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return runtimefence.ErrStale
	}
	return err
}

func channelIdentityExists(ctx context.Context, identities ChannelIdentityReader, id string) (bool, error) {
	if identities == nil {
		return false, errors.New("channel identity reader is required")
	}
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return false, nil
	}
	if _, err := parseUUID(trimmed); err != nil {
		return false, err
	}
	_, err := identities.GetByID(ctx, trimmed)
	if err != nil {
		return false, nil
	}
	return true, nil
}

// AllowAllChannelIdentities is a ChannelIdentityReader that accepts any ID.
// Use only in tests that do not exercise identity validation.
func AllowAllChannelIdentities() ChannelIdentityReader {
	return allowAllChannelIdentities{}
}

type allowAllChannelIdentities struct{}

func (allowAllChannelIdentities) GetByID(_ context.Context, id string) (ChannelIdentity, error) {
	return ChannelIdentity{ID: id}, nil
}
