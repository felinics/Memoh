package db

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	// Register postgres driver for golang-migrate.
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/felinics/memoh/internal/config"
)

type MigrationStatus struct {
	Version uint
	Dirty   bool
}

// RunMigrate applies or rolls back database migrations.
// The migrationsFS should contain .sql files at its root (not in a subdirectory).
// Supported commands: "up", "down", "version", "force N".
func RunMigrate(logger *slog.Logger, cfg config.PostgresConfig, migrationsFS fs.FS, command string, args []string) error {
	return RunMigrateTarget(logger, MigrationTarget{Driver: DriverPostgres, DSN: DSN(cfg)}, migrationsFS, command, args)
}

func RunMigrateConfig(logger *slog.Logger, cfg config.Config, migrationsFS fs.FS, command string, args []string) error {
	target, err := MigrationTargetFromConfig(cfg)
	if err != nil {
		return err
	}
	return RunMigrateTarget(logger, target, migrationsFS, command, args)
}

func RunMigrateTarget(logger *slog.Logger, target MigrationTarget, migrationsFS fs.FS, command string, args []string) error {
	switch command {
	case "up", "down", "version", "force":
	default:
		return fmt.Errorf("unknown migrate command: %s (use: up, down, version, force)", command)
	}
	if command == "force" && len(args) == 0 {
		return errors.New("force requires a version number argument")
	}
	if target.DSN == "" {
		return errors.New("migration target DSN is empty")
	}
	if logger == nil {
		logger = slog.Default()
	}

	sourceDriver, err := iofs.New(migrationsFS, ".")
	if err != nil {
		return fmt.Errorf("migration source: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", sourceDriver, target.DSN)
	if err != nil {
		return fmt.Errorf("migrate init: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	m.Log = &migrateLogger{logger: logger}

	switch command {
	case "up":
		if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("migrate up: %w", err)
		}
		ver, dirty, _ := m.Version()
		logger.Info("migration complete", slog.Uint64("version", uint64(ver)), slog.Bool("dirty", dirty))

	case "down":
		if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("migrate down: %w", err)
		}
		logger.Info("all migrations rolled back")

	case "version":
		ver, dirty, err := m.Version()
		if err != nil {
			return fmt.Errorf("migrate version: %w", err)
		}
		logger.Info("current version", slog.Uint64("version", uint64(ver)), slog.Bool("dirty", dirty))

	case "force":
		var version int
		if _, err := fmt.Sscanf(args[0], "%d", &version); err != nil {
			return fmt.Errorf("invalid version: %w", err)
		}
		if err := m.Force(version); err != nil {
			return fmt.Errorf("migrate force: %w", err)
		}
		logger.Info("forced version", slog.Int("version", version))
	}

	return nil
}

func ReadMigrationStatus(cfg config.PostgresConfig, migrationsFS fs.FS) (MigrationStatus, error) {
	return ReadMigrationStatusTarget(MigrationTarget{Driver: DriverPostgres, DSN: DSN(cfg)}, migrationsFS)
}

func ReadMigrationStatusConfig(cfg config.Config, migrationsFS fs.FS) (MigrationStatus, error) {
	target, err := MigrationTargetFromConfig(cfg)
	if err != nil {
		return MigrationStatus{}, err
	}
	return ReadMigrationStatusTarget(target, migrationsFS)
}

func ReadMigrationStatusTarget(target MigrationTarget, migrationsFS fs.FS) (MigrationStatus, error) {
	if target.DSN == "" {
		return MigrationStatus{}, errors.New("migration target DSN is empty")
	}
	sourceDriver, err := iofs.New(migrationsFS, ".")
	if err != nil {
		return MigrationStatus{}, fmt.Errorf("migration source: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", sourceDriver, target.DSN)
	if err != nil {
		return MigrationStatus{}, fmt.Errorf("migrate init: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	ver, dirty, err := m.Version()
	if err != nil {
		if errors.Is(err, migrate.ErrNilVersion) {
			return MigrationStatus{}, nil
		}
		return MigrationStatus{}, fmt.Errorf("migrate version: %w", err)
	}
	return MigrationStatus{
		Version: ver,
		Dirty:   dirty,
	}, nil
}

type migrateLogger struct {
	logger *slog.Logger
}

func (l *migrateLogger) Printf(format string, v ...interface{}) {
	l.logger.Info("migration log", slog.String("detail", fmt.Sprintf(format, v...)))
}

func (*migrateLogger) Verbose() bool {
	return false
}
