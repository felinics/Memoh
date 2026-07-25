package epoch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

const advisoryLockID int64 = 0x4d656d6f684532

// State classifies the repository database migration state.
type State string

const (
	StateEmpty   State = "empty"
	StateV1      State = "v1"
	StateV2      State = "v2"
	StatePartial State = "partial"
)

// LegacyStatus describes the frozen v1 migration ledger when present.
type LegacyStatus struct {
	Exists  bool   `json:"exists"`
	Version uint64 `json:"version"`
	Dirty   bool   `json:"dirty"`
}

// OwnerStatus is a read-only snapshot of one owner migration stream.
type OwnerStatus struct {
	Owner          string `json:"owner"`
	Schema         string `json:"schema"`
	VersionTable   string `json:"version_table"`
	Exists         bool   `json:"exists"`
	CurrentVersion int64  `json:"current_version"`
	TargetVersion  int64  `json:"target_version"`
	Pending        bool   `json:"pending"`
}

// DatabaseStatus is the typed repository-wide migration snapshot.
type DatabaseStatus struct {
	Epoch  int           `json:"epoch"`
	State  State         `json:"state"`
	Legacy LegacyStatus  `json:"legacy"`
	Owners []OwnerStatus `json:"owners"`
}

// Runner applies and verifies the repository-wide Epoch v2 migration plan.
type Runner struct {
	db     *sql.DB
	fsys   fs.FS
	logger *slog.Logger
}

// New constructs a Runner and validates its current manifest and assets.
func New(db *sql.DB, fsys fs.FS, logger *slog.Logger) (*Runner, error) {
	if db == nil {
		return nil, errors.New("create epoch runner: database is nil")
	}
	if fsys == nil {
		return nil, errors.New("create epoch runner: filesystem is nil")
	}
	if logger == nil {
		return nil, errors.New("create epoch runner: logger is nil")
	}
	if _, err := Load(fsys); err != nil {
		return nil, fmt.Errorf("create epoch runner: %w", err)
	}
	return &Runner{db: db, fsys: fsys, logger: logger}, nil
}

// Up applies a fresh Epoch v2 database or verifies an already complete one.
func (r *Runner) Up(ctx context.Context) (retErr error) {
	manifest, err := Load(r.fsys)
	if err != nil {
		return fmt.Errorf("up load manifest: %w", err)
	}
	if err := r.requireProviderConnection(); err != nil {
		return fmt.Errorf("up: %w", err)
	}

	status, err := r.Status(ctx)
	if err != nil {
		return fmt.Errorf("up detect state: %w", err)
	}
	switch status.State {
	case StateV2:
		return r.Verify(ctx)
	case StateEmpty:
	case StateV1:
		return errors.New("up: legacy v1 database requires migrate upgrade-v2")
	default:
		return fmt.Errorf("up: refuse database state %q", status.State)
	}

	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("up acquire dedicated connection: %w", err)
	}
	defer func() {
		retErr = errors.Join(retErr, conn.Close())
	}()
	if err := acquireAdvisoryLock(ctx, conn); err != nil {
		return fmt.Errorf("up acquire repository advisory lock: %w", err)
	}
	defer func() {
		retErr = errors.Join(retErr, releaseAdvisoryLock(ctx, conn))
	}()

	lockedState, _, _, err := inspectState(ctx, conn, manifest)
	if err != nil {
		return fmt.Errorf("up recheck state: %w", err)
	}
	if lockedState != StateEmpty {
		return fmt.Errorf("up: database state changed to %q while acquiring lock", lockedState)
	}
	for _, owner := range manifest.Owners {
		if err := bootstrapOwnerSchema(ctx, conn, owner); err != nil {
			return fmt.Errorf("owner %q step bootstrap schema: %w", owner.Name, err)
		}
		if err := r.upOwner(ctx, owner); err != nil {
			return err
		}
	}
	if err := r.verifyManifest(ctx); err != nil {
		return fmt.Errorf("up verify: %w", err)
	}
	return nil
}

type schemaExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func bootstrapOwnerSchema(ctx context.Context, execer schemaExecer, owner Owner) error {
	// Load validates Schema against the fixed owner allowlist before Up reaches this point.
	if _, err := execer.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+owner.Schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if owner.Name == "iam" {
		return nil
	}
	// Goose creates the owner ledger as the deploying login before it acquires
	// the memoh_migrate session lock. Give that role access to the new ledger;
	// the owner baseline subsequently normalizes ownership and final privileges.
	for _, objectType := range []string{"TABLES", "SEQUENCES"} {
		query := `ALTER DEFAULT PRIVILEGES IN SCHEMA ` + owner.Schema +
			` GRANT ALL ON ` + objectType + ` TO memoh_migrate`
		if _, err := execer.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("grant migrate defaults on %s: %w", strings.ToLower(objectType), err)
		}
	}
	return nil
}

// Status returns a typed snapshot without printing it.
func (r *Runner) Status(ctx context.Context) (DatabaseStatus, error) {
	manifest, err := Load(r.fsys)
	if err != nil {
		return DatabaseStatus{}, fmt.Errorf("status load manifest: %w", err)
	}
	state, legacy, existing, err := inspectState(ctx, r.db, manifest)
	if err != nil {
		return DatabaseStatus{}, fmt.Errorf("status inspect database: %w", err)
	}
	status := DatabaseStatus{
		Epoch:  manifest.Epoch,
		State:  state,
		Legacy: legacy,
		Owners: make([]OwnerStatus, 0, len(manifest.Owners)),
	}
	for _, owner := range manifest.Owners {
		snapshot := OwnerStatus{
			Owner:         owner.Name,
			Schema:        owner.Schema,
			VersionTable:  owner.VersionTable,
			Exists:        existing[owner.Name],
			TargetVersion: owner.Version,
			Pending:       true,
		}
		if snapshot.Exists && state == StateV2 {
			current, pending, err := r.ownerVersionStatus(ctx, owner)
			if err != nil {
				return DatabaseStatus{}, fmt.Errorf("owner %q action status: %w", owner.Name, err)
			}
			snapshot.CurrentVersion = current
			snapshot.Pending = pending
		}
		status.Owners = append(status.Owners, snapshot)
	}
	return status, nil
}

// Verify requires a complete Epoch v2 state with every owner exactly at target.
func (r *Runner) Verify(ctx context.Context) error {
	if _, err := Load(r.fsys); err != nil {
		return fmt.Errorf("verify load manifest: %w", err)
	}
	return r.verifyManifest(ctx)
}

func (r *Runner) verifyManifest(ctx context.Context) error {
	status, err := r.Status(ctx)
	if err != nil {
		return err
	}
	if status.State != StateV2 {
		return fmt.Errorf("verify database state: got %q, want %q", status.State, StateV2)
	}
	for _, owner := range status.Owners {
		if owner.CurrentVersion != owner.TargetVersion {
			return fmt.Errorf(
				"owner %q action verify version: got %d, want %d",
				owner.Owner,
				owner.CurrentVersion,
				owner.TargetVersion,
			)
		}
		if owner.Pending {
			return fmt.Errorf(
				"owner %q action verify version %d: pending migration",
				owner.Owner,
				owner.TargetVersion,
			)
		}
	}
	return nil
}

func (r *Runner) upOwner(ctx context.Context, owner Owner) error {
	asMigrate := owner.Name != "iam"
	provider, err := r.newProvider(owner, asMigrate)
	if err != nil {
		return fmt.Errorf("owner %q action create provider: %w", owner.Name, err)
	}
	started := time.Now()
	results, err := provider.Up(ctx)
	for _, result := range results {
		outcome := "ok"
		if result.Empty {
			outcome = "empty"
		}
		r.logger.InfoContext(
			ctx,
			"database migration",
			slog.String("owner", owner.Name),
			slog.Int64("from", result.Source.Version-1),
			slog.Int64("to", owner.Version),
			slog.Int64("version", result.Source.Version),
			slog.Duration("duration", result.Duration),
			slog.String("result", outcome),
		)
	}
	if err != nil {
		r.logger.ErrorContext(
			ctx,
			"database migration",
			slog.String("owner", owner.Name),
			slog.Int64("from", 0),
			slog.Int64("to", owner.Version),
			slog.Int64("version", 0),
			slog.Duration("duration", time.Since(started)),
			slog.String("result", "failed"),
		)
		return fmt.Errorf("owner %q action up version %d: %w", owner.Name, owner.Version, err)
	}
	if len(results) == 0 {
		return fmt.Errorf("owner %q action up version %d: no migration applied", owner.Name, owner.Version)
	}
	return nil
}

func (r *Runner) ownerVersionStatus(ctx context.Context, owner Owner) (int64, bool, error) {
	provider, err := r.newProvider(owner, true)
	if err != nil {
		return 0, false, err
	}
	current, err := provider.GetDBVersion(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("get database version: %w", err)
	}
	migrations, err := provider.Status(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("get migration status: %w", err)
	}
	for _, migration := range migrations {
		if migration.State == goose.StatePending {
			return current, true, nil
		}
	}
	return current, false, nil
}

func (r *Runner) newProvider(owner Owner, asMigrate bool) (*goose.Provider, error) {
	migrations, err := fs.Sub(r.fsys, owner.MigrationsDir)
	if err != nil {
		return nil, fmt.Errorf("open migrations_dir: %w", err)
	}
	options := []goose.ProviderOption{
		goose.WithTableName(owner.VersionTable),
		goose.WithDisableGlobalRegistry(true),
		goose.WithAllowOutofOrder(false),
		goose.WithSlog(r.logger),
	}
	if asMigrate {
		var locker lock.SessionLocker = migrateRoleSession{}
		options = append(options, goose.WithSessionLocker(locker))
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, r.db, migrations, options...)
	if err != nil {
		return nil, fmt.Errorf("initialize goose provider: %w", err)
	}
	return provider, nil
}

func (r *Runner) requireProviderConnection() error {
	if r.db.Stats().MaxOpenConnections == 1 {
		return errors.New("database pool requires at least two open connections")
	}
	return nil
}

type migrateRoleSession struct{}

func (migrateRoleSession) SessionLock(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `SET ROLE memoh_migrate`); err != nil {
		return fmt.Errorf("set role memoh_migrate: %w", err)
	}
	return nil
}

func (migrateRoleSession) SessionUnlock(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `RESET ROLE`); err != nil {
		return fmt.Errorf("reset role: %w", err)
	}
	return nil
}

type stateQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func inspectState(
	ctx context.Context,
	queryer stateQueryer,
	manifest Manifest,
) (State, LegacyStatus, map[string]bool, error) {
	legacy := LegacyStatus{}
	if err := queryer.QueryRowContext(
		ctx,
		`SELECT to_regclass('public.schema_migrations') IS NOT NULL`,
	).Scan(&legacy.Exists); err != nil {
		return "", LegacyStatus{}, nil, err
	}
	if legacy.Exists {
		if err := queryer.QueryRowContext(
			ctx,
			`SELECT version, dirty FROM public.schema_migrations`,
		).Scan(&legacy.Version, &legacy.Dirty); err != nil {
			return "", LegacyStatus{}, nil, fmt.Errorf("read public.schema_migrations: %w", err)
		}
	}

	existing := make(map[string]bool, len(manifest.Owners))
	versionTables := 0
	ownerSchemas := 0
	for _, owner := range manifest.Owners {
		var schemaExists, tableExists bool
		if err := queryer.QueryRowContext(
			ctx,
			`SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname = $1), to_regclass($2) IS NOT NULL`,
			owner.Schema,
			owner.VersionTable,
		).Scan(&schemaExists, &tableExists); err != nil {
			return "", LegacyStatus{}, nil, fmt.Errorf("inspect owner %q: %w", owner.Name, err)
		}
		if schemaExists {
			ownerSchemas++
		}
		if tableExists {
			versionTables++
			existing[owner.Name] = true
		}
	}
	var publicRelations int
	if versionTables != len(manifest.Owners) && ownerSchemas == 0 && !legacy.Exists {
		if err := queryer.QueryRowContext(ctx, `
			SELECT count(*)
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public' AND c.relkind IN ('r','p','v','m','S')
		`).Scan(&publicRelations); err != nil {
			return "", LegacyStatus{}, nil, fmt.Errorf("inspect public objects: %w", err)
		}
	}
	return classifyState(
		len(manifest.Owners),
		versionTables,
		ownerSchemas,
		publicRelations,
		legacy.Exists,
	), legacy, existing, nil
}

func classifyState(
	ownerCount int,
	versionTables int,
	ownerSchemas int,
	publicRelations int,
	legacyExists bool,
) State {
	if versionTables == ownerCount {
		return StateV2
	}
	if versionTables > 0 || ownerSchemas > 0 || publicRelations > 0 {
		return StatePartial
	}
	if legacyExists {
		return StateV1
	}
	return StateEmpty
}

func acquireAdvisoryLock(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockID); err != nil {
		return err
	}
	return nil
}

func releaseAdvisoryLock(ctx context.Context, conn *sql.Conn) error {
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var unlocked bool
	if err := conn.QueryRowContext(
		unlockCtx,
		`SELECT pg_advisory_unlock($1)`,
		advisoryLockID,
	).Scan(&unlocked); err != nil {
		return fmt.Errorf("release repository advisory lock: %w", err)
	}
	if !unlocked {
		return errors.New("release repository advisory lock: lock was not held")
	}
	return nil
}
