package dbtest

import (
	"fmt"
	"io/fs"
	"log/slog"

	dbembed "github.com/memohai/memoh/db"
	dbpkg "github.com/memohai/memoh/internal/db"
)

// MigratePostgresLegacyUp applies the frozen Epoch v1 chain for tests that
// still exercise public-schema adapters.
func MigratePostgresLegacyUp(dsn string) error {
	migrations, err := fs.Sub(dbembed.MigrationsFS, "postgres/legacy/v1/migrations")
	if err != nil {
		return fmt.Errorf("open embedded PostgreSQL migrations: %w", err)
	}
	logger := slog.New(slog.DiscardHandler)
	if err := dbpkg.RunMigrateTarget(logger, dbpkg.MigrationTarget{
		Driver: dbpkg.DriverPostgres,
		DSN:    dsn,
	}, migrations, "up", nil); err != nil {
		return fmt.Errorf("migrate PostgreSQL test database: %w", err)
	}
	return nil
}
