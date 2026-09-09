package db

import (
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/config"
)

const (
	DriverPostgres = "postgres"
)

type MigrationTarget struct {
	Driver string
	DSN    string
}

func DriverFromConfig(cfg config.Config) string {
	return strings.TrimSpace(strings.ToLower(cfg.Database.DriverOrDefault()))
}

func MigrationTargetFromConfig(cfg config.Config) (MigrationTarget, error) {
	switch driver := DriverFromConfig(cfg); driver {
	case DriverPostgres:
		return MigrationTarget{Driver: DriverPostgres, DSN: DSN(cfg.Postgres)}, nil
	default:
		return MigrationTarget{}, fmt.Errorf("unsupported database driver %q", driver)
	}
}
