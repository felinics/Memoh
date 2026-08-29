package pgvector

import (
	"fmt"
	"io/fs"
	"log/slog"

	dbembed "github.com/felinics/memoh/db"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db"
)

const migrationsPath = "pgvector/migrations"

// SchemaVersion is the newest pgvector migration understood by this binary.
const SchemaVersion = uint(2)

// MigrationsFS returns the independently versioned pgvector migration set.
func MigrationsFS() (fs.FS, error) {
	migrations, err := fs.Sub(dbembed.MigrationsFS, migrationsPath)
	if err != nil {
		return nil, fmt.Errorf("pgvector migrations: %w", err)
	}
	return migrations, nil
}

// MigrateUp upgrades the configured optional pgvector database. It is kept
// separate from the primary PostgreSQL migration stream because the two
// databases can have different hosts, credentials, and lifecycles.
func MigrateUp(logger *slog.Logger, cfg config.PGVectorConfig) error {
	migrations, err := MigrationsFS()
	if err != nil {
		return err
	}
	status, err := db.ReadMigrationStatus(cfg.PostgresConfig(), migrations)
	if err != nil {
		return fmt.Errorf("pgvector migration status: %w", err)
	}
	if err := validateMigrationStatus(status, false); err != nil {
		return err
	}
	if err := db.RunMigrate(logger, cfg.PostgresConfig(), migrations, "up", nil); err != nil {
		return fmt.Errorf("pgvector migrations: %w", err)
	}
	status, err = db.ReadMigrationStatus(cfg.PostgresConfig(), migrations)
	if err != nil {
		return fmt.Errorf("pgvector migration status: %w", err)
	}
	return validateMigrationStatus(status, true)
}

func validateMigrationStatus(status db.MigrationStatus, requireCurrent bool) error {
	if status.Dirty {
		return fmt.Errorf("pgvector schema version %d is dirty", status.Version)
	}
	if status.Version > SchemaVersion {
		return fmt.Errorf("pgvector schema version %d is newer than supported version %d", status.Version, SchemaVersion)
	}
	if requireCurrent && status.Version != SchemaVersion {
		return fmt.Errorf("pgvector schema stopped at version %d, want %d", status.Version, SchemaVersion)
	}
	return nil
}
