package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/crypto/bcrypt"

	dbembed "github.com/felinics/memoh/db"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/logger"
	"github.com/felinics/memoh/internal/version"
)

func provideConfig() (config.Config, error) {
	cfgPath := os.Getenv("CONFIG_PATH")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return config.Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func migrationsFS(cfg config.Config) fs.FS {
	sub, err := db.MigrationsFSForConfig(cfg, dbembed.MigrationsFS)
	if err != nil {
		panic(fmt.Sprintf("embedded migrations: %v", err))
	}
	return sub
}

func runMigrateCommand(args []string) error {
	cfg, err := provideConfig()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger.Init(cfg.Log.Level, cfg.Log.Format)
	log := logger.L

	migrateCmd := args[0]
	var migrateArgs []string
	if len(args) > 1 {
		migrateArgs = args[1:]
	}

	if err := db.RunMigrateConfig(log, cfg, migrationsFS(cfg), migrateCmd, migrateArgs); err != nil {
		log.Error("migration failed", slog.Any("error", err))
		return err
	}
	return nil
}

func runAccountCommand(args []string, passwordInput io.Reader) error {
	if len(args) != 2 || args[0] != "recover-admin" {
		return errors.New("usage: memoh-server account recover-admin <username-or-email> < new-password-file")
	}
	identity := strings.TrimSpace(args[1])
	if identity == "" {
		return errors.New("username or email is required")
	}
	passwordBytes, err := io.ReadAll(io.LimitReader(passwordInput, 4097))
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	if len(passwordBytes) > 4096 {
		return errors.New("password exceeds 4096 bytes")
	}
	password := strings.TrimRight(string(passwordBytes), "\r\n")
	if password == "" {
		return errors.New("new password is required on stdin")
	}

	cfg, err := provideConfig()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, cfg)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin recovery: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	queries := dbsqlc.New(tx)
	account, err := queries.GetAccountByIdentity(ctx, pgtype.Text{String: identity, Valid: true})
	if err != nil {
		return fmt.Errorf("find account: %w", err)
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE users
		   SET password_hash=$1, is_active=true, updated_at=now()
		 WHERE id=$2`, string(hashed), account.ID); err != nil {
		return fmt.Errorf("update credentials: %w", err)
	}
	if _, err := queries.UpdateAccountAdmin(ctx, dbsqlc.UpdateAccountAdminParams{
		UserID:   account.ID,
		Role:     "admin",
		IsActive: pgtype.Bool{Bool: true, Valid: true},
	}); err != nil {
		return fmt.Errorf("restore admin membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit recovery: %w", err)
	}
	return nil
}

func runVersion() error {
	fmt.Printf("memoh-server %s\n", version.GetInfo())
	return nil
}
