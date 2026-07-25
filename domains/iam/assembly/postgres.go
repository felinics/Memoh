// Package assembly exposes IAM composition constructors.
package assembly

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/iam/account"
	accountpostgres "github.com/memohai/memoh/domains/iam/internal/account/postgres"
	dbsqlc "github.com/memohai/memoh/domains/iam/internal/postgres/sqlc"
)

// NewAccountStore builds IAM account persistence.
func NewAccountStore(pool *pgxpool.Pool) account.Store {
	return accountpostgres.NewStore(dbsqlc.New(pool))
}

// NewAccountCounter builds IAM account counting persistence.
func NewAccountCounter(pool *pgxpool.Pool) account.AccountCounter {
	return accountpostgres.NewStore(dbsqlc.New(pool))
}

// NewAccountRecovery builds admin account recovery.
func NewAccountRecovery(pool *pgxpool.Pool) *account.Recovery {
	return account.NewRecovery(accountpostgres.NewRecoveryStore(pool))
}
