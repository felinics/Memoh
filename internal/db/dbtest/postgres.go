package dbtest

import (
	"fmt"
	"io/fs"
	"log/slog"

	dbembed "github.com/felinics/memoh/db"
	dbpkg "github.com/felinics/memoh/internal/db"
)

// MigratePostgresUp applies the same embedded migration chain used by the server.
func MigratePostgresUp(dsn string) error {
	migrations, err := fs.Sub(dbembed.MigrationsFS, "postgres/migrations")
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
