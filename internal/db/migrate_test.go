package db

import (
	"io/fs"
	"testing"

	"github.com/golang-migrate/migrate/v4/source/iofs"

	dbembed "github.com/felinics/memoh/db"
	"github.com/felinics/memoh/internal/config"
)

func TestEmbeddedMigrationsHaveUniqueVersions(t *testing.T) {
	migrations, err := fs.Sub(dbembed.MigrationsFS, "postgres/migrations")
	if err != nil {
		t.Fatalf("open embedded migrations: %v", err)
	}

	driver, err := iofs.New(migrations, ".")
	if err != nil {
		t.Fatalf("initialize embedded migrations: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
}

func TestRunMigrateUnknownCommand(t *testing.T) {
	cfg := config.PostgresConfig{
		Host:     "localhost",
		Port:     5432,
		User:     "memoh",
		Password: "secret",
		Database: "memoh",
		SSLMode:  "disable",
	}
	err := RunMigrate(nil, cfg, nil, "invalid", nil)
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
}
