package db

import (
	"fmt"
	"io/fs"

	"github.com/memohai/memoh/internal/config"
)

func LegacyMigrationsFSForConfig(cfg config.Config, embedded fs.FS) (fs.FS, error) {
	switch driver := DriverFromConfig(cfg); driver {
	case DriverPostgres:
		// Epoch v1 ledger is archived under legacy/v1; golang-migrate continues
		// to consume that immutable file set until upgrade-v2 completes.
		return fs.Sub(embedded, "postgres/legacy/v1/migrations")
	default:
		return nil, fmt.Errorf("unsupported database driver %q", driver)
	}
}
