// Package cluster constructs the Agent decision persistence ports that share
// one transaction cluster.
//
// It sits beside decision rather than inside it because approval and input
// already import decision; a constructor in the root would close a cycle.
package cluster

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	runtimefence "github.com/memohai/memoh/domains/agent/chat/session/fence"
	"github.com/memohai/memoh/domains/agent/decision/approval"
	"github.com/memohai/memoh/domains/agent/decision/input"
	decisionpostgres "github.com/memohai/memoh/domains/agent/internal/postgres/decision"
)

// BotSessionWriteLocker locks the API-owned bot row (FOR KEY SHARE) before
// Agent session/decision writes.
//
// Owner: API (api.bots). Agent must not query that table directly, so
// composition supplies the implementation.
type BotSessionWriteLocker interface {
	LockBotForSessionWrite(context.Context, pgtype.UUID) (pgtype.UUID, error)
}

// BotSessionWriteLockerFromTx binds a BotSessionWriteLocker to an open
// transaction.
type BotSessionWriteLockerFromTx func(pgx.Tx) BotSessionWriteLocker

// ChannelIdentity is the minimal inbound identity projection decision
// persistence needs for existence checks.
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

// Cluster exposes the decision persistence ports that share one transaction
// cluster: approval, user input, and the runtime fence that orders them.
//
// The three ports are constructed together because they lock api.bots and the
// session in one order; splitting them into independent constructors would let
// a caller assemble that ordering wrongly.
type Cluster interface {
	Approval() approval.Persistence
	Input() input.Persistence
	RuntimeFence() runtimefence.Persistence
}

// NewPostgres constructs the Agent-owned decision persistence cluster backed
// by PostgreSQL.
func NewPostgres(
	pool *pgxpool.Pool,
	newLock BotSessionWriteLockerFromTx,
	identities ChannelIdentityReader,
) (Cluster, error) {
	return decisionpostgres.New(
		pool,
		func(tx pgx.Tx) decisionpostgres.BotSessionWriteLocker { return newLock(tx) },
		channelIdentityReader{reader: identities},
	)
}

// channelIdentityReader adapts the public reader port to the owner-private
// adapter's own projection type.
type channelIdentityReader struct {
	reader ChannelIdentityReader
}

func (r channelIdentityReader) GetByID(ctx context.Context, id string) (decisionpostgres.ChannelIdentity, error) {
	identity, err := r.reader.GetByID(ctx, id)
	if err != nil {
		return decisionpostgres.ChannelIdentity{}, err
	}
	return decisionpostgres.ChannelIdentity{ID: identity.ID, DisplayName: identity.DisplayName}, nil
}
