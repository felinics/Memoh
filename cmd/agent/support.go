package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"

	dbembed "github.com/memohai/memoh/db"
	emailpkg "github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/domains/iam/account"
	iamassembly "github.com/memohai/memoh/domains/iam/assembly"
	"github.com/memohai/memoh/internal/config"
	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/epoch"
	pgvectordb "github.com/memohai/memoh/internal/db/pgvector"
	"github.com/memohai/memoh/internal/logger"
	"github.com/memohai/memoh/internal/version"
)

func provideConfig() (config.Config, error) {
	cfgPath := os.Getenv("CONFIG_PATH")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return config.Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func postgresMigrationsFS() fs.FS {
	sub, err := fs.Sub(dbembed.MigrationsFS, "postgres")
	if err != nil {
		panic(fmt.Sprintf("embedded migrations: %v", err))
	}
	return sub
}

func runMigrateCommand(args []string, output io.Writer) error {
	command, err := parseMigrateCommand(args)
	if err != nil {
		return err
	}
	cfg, err := provideConfig()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if db.DriverFromConfig(cfg) != db.DriverPostgres {
		return fmt.Errorf("migrate: unsupported database driver %q", db.DriverFromConfig(cfg))
	}

	logger.Init(cfg.Log.Level, cfg.Log.Format)
	log := logger.L

	pgxConfig, err := pgx.ParseConfig(db.DSN(cfg.Postgres))
	if err != nil {
		return fmt.Errorf("migrate parse PostgreSQL config: %w", err)
	}
	migrationDB := stdlib.OpenDB(*pgxConfig)
	defer func() { _ = migrationDB.Close() }()

	runner, err := epoch.New(migrationDB, postgresMigrationsFS(), log)
	if err != nil {
		return fmt.Errorf("migrate create runner: %w", err)
	}

	switch command {
	case "up":
		err = runner.Up(context.Background())
	case "status":
		var status epoch.DatabaseStatus
		status, err = runner.Status(context.Background())
		if err == nil {
			err = json.NewEncoder(output).Encode(status)
		}
	case "verify":
		err = runner.Verify(context.Background())
	case "upgrade-v2":
		err = runner.UpgradeV2(context.Background())
	}
	if err != nil {
		log.Error("migration failed", slog.Any("error", err))
		return err
	}
	if cfg.PGVector.Enabled && command == "up" {
		if err := pgvectordb.MigrateUp(log, cfg.PGVector); err != nil {
			log.Error("pgvector migration failed", slog.Any("error", err))
			return err
		}
	}
	return nil
}

func parseMigrateCommand(args []string) (string, error) {
	if len(args) != 1 {
		return "", errors.New("usage: memoh-server migrate <up|status|verify|upgrade-v2>")
	}
	switch args[0] {
	case "up", "status", "verify", "upgrade-v2":
		return args[0], nil
	default:
		return "", fmt.Errorf(
			"unknown migrate command %q (use: up, status, verify, upgrade-v2)",
			args[0],
		)
	}
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
	recovery := iamassembly.NewAccountRecovery(pool)
	return recovery.RecoverAdmin(ctx, identity, password)
}

func ensureAdminUser(
	ctx context.Context,
	log *slog.Logger,
	counter account.AccountCounter,
	creator account.Store,
	emailService *emailpkg.Service,
	cfg config.Config,
) error {
	if counter == nil {
		return errors.New("account counter not configured")
	}
	if creator == nil {
		return errors.New("account creator not configured")
	}
	count, err := counter.CountAccounts(ctx)
	if err != nil {
		return fmt.Errorf("count accounts: %w", err)
	}
	if count > 0 {
		return nil
	}

	username := strings.TrimSpace(cfg.Admin.Username)
	password := strings.TrimSpace(cfg.Admin.Password)
	email := strings.TrimSpace(cfg.Admin.Email)
	if username == "" || password == "" {
		return errors.New("admin username/password required in config.toml")
	}
	if password == "change-your-password-here" {
		log.WarnContext(ctx, "admin password uses default placeholder; please update config.toml")
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}

	user, err := creator.CreateUser(ctx, account.CreateUserInput{
		IsActive: true,
		Metadata: []byte("{}"),
	})
	if err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}

	_, err = creator.CreateAccount(ctx, account.CreateInput{
		UserID:       user.ID,
		Username:     username,
		Email:        email,
		PasswordHash: string(hashed),
		Role:         "admin",
		DisplayName:  username,
		IsActive:     true,
	})
	if err != nil {
		return fmt.Errorf("create admin account: %w", err)
	}
	if emailService != nil {
		if err := emailService.EnsureDefaultGmailProvider(ctx, user.ID); err != nil {
			return fmt.Errorf("ensure admin gmail provider: %w", err)
		}
	}
	log.InfoContext(ctx, "Admin user created", slog.String("username", username))
	return nil
}

func runVersion() error {
	fmt.Printf("memoh-server %s\n", version.GetInfo(buildProfile))
	return nil
}
