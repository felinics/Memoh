package epoch

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/pressly/goose/v3/database"
	"gopkg.in/yaml.v3"
)

const bridgeRoot = "legacy/v1/upgrade/to_v2"

var bridgeStepIDs = []string{
	"preflight",
	"create_schemas",
	"drop_cross_owner_fks",
	"move_types_functions_sequences",
	"move_tables",
	"fix_platform_functions",
	"verify",
	"stamp_ready",
}

type bridgePlan struct {
	FromEpoch int            `yaml:"from_epoch"`
	ToEpoch   int            `yaml:"to_epoch"`
	Requires  bridgeRequires `yaml:"requires"`
	Steps     []bridgeStep   `yaml:"steps"`
}

type bridgeRequires struct {
	SchemaMigrationsVersion uint64 `yaml:"schema_migrations_version"`
	Dirty                   bool   `yaml:"dirty"`
}

type bridgeStep struct {
	ID               string `yaml:"id"`
	SQL              string `yaml:"sql"`
	CrossOwnerFKList string `yaml:"cross_owner_fk_list,omitempty"`
	StampOwnersTo    int64  `yaml:"stamp_owners_to,omitempty"`
	Note             string `yaml:"note,omitempty"`
}

// UpgradeV2 atomically bridges an approved v1 database into Epoch v2.
func (r *Runner) UpgradeV2(ctx context.Context) (retErr error) {
	manifest, err := Load(r.fsys)
	if err != nil {
		return fmt.Errorf("upgrade-v2 load manifest: %w", err)
	}
	plan, err := loadBridgePlan(r.fsys)
	if err != nil {
		return fmt.Errorf("upgrade-v2 load plan: %w", err)
	}
	if err := r.requireProviderConnection(); err != nil {
		return fmt.Errorf("upgrade-v2: %w", err)
	}

	state, legacy, _, err := inspectState(ctx, r.db, manifest)
	if err != nil {
		return fmt.Errorf("upgrade-v2 detect state: %w", err)
	}
	if state == StateV2 {
		return r.Verify(ctx)
	}
	if err := validateBridgeSource(state, legacy, plan); err != nil {
		return fmt.Errorf("upgrade-v2: %w", err)
	}

	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("upgrade-v2 acquire dedicated connection: %w", err)
	}
	defer func() {
		retErr = errors.Join(retErr, conn.Close())
	}()
	if err := acquireAdvisoryLock(ctx, conn); err != nil {
		return fmt.Errorf("upgrade-v2 acquire repository advisory lock: %w", err)
	}
	defer func() {
		retErr = errors.Join(retErr, releaseAdvisoryLock(ctx, conn))
	}()

	lockedState, lockedLegacy, _, err := inspectState(ctx, conn, manifest)
	if err != nil {
		return fmt.Errorf("upgrade-v2 recheck state: %w", err)
	}
	if err := validateBridgeSource(lockedState, lockedLegacy, plan); err != nil {
		return fmt.Errorf("upgrade-v2 recheck: %w", err)
	}
	if err := r.runBridgeTransaction(ctx, conn, manifest, plan); err != nil {
		return err
	}
	if err := r.verifyManifest(ctx); err != nil {
		return fmt.Errorf("upgrade-v2 verify: %w", err)
	}
	return nil
}

func (r *Runner) runBridgeTransaction(
	ctx context.Context,
	conn *sql.Conn,
	manifest Manifest,
	plan bridgePlan,
) (retErr error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("upgrade-v2 begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			retErr = errors.Join(retErr, tx.Rollback())
		}
	}()

	for i, step := range plan.Steps {
		sqlPath := path.Join(bridgeRoot, step.SQL)
		content, err := fs.ReadFile(r.fsys, sqlPath)
		if err != nil {
			return fmt.Errorf(
				"upgrade-v2 step %q version %02d read %q: %w",
				step.ID,
				i+1,
				sqlPath,
				err,
			)
		}
		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			return fmt.Errorf(
				"upgrade-v2 step %q version %02d execute: %w",
				step.ID,
				i+1,
				err,
			)
		}
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL ROLE memoh_migrate`); err != nil {
		return fmt.Errorf("upgrade-v2 step stamp version 1 set role: %w", err)
	}
	for _, owner := range manifest.Owners {
		if owner.Version != 1 {
			return fmt.Errorf(
				"owner %q step stamp version %d: bridge only supports version 1",
				owner.Name,
				owner.Version,
			)
		}
		store, err := database.NewStore(database.DialectPostgres, owner.VersionTable)
		if err != nil {
			return fmt.Errorf("owner %q step create version store: %w", owner.Name, err)
		}
		if err := store.CreateVersionTable(ctx, tx); err != nil {
			return fmt.Errorf("owner %q step create version table: %w", owner.Name, err)
		}
		if err := store.Insert(ctx, tx, database.InsertRequest{Version: 0}); err != nil {
			return fmt.Errorf("owner %q step stamp version 0: %w", owner.Name, err)
		}
		if err := store.Insert(ctx, tx, database.InsertRequest{Version: owner.Version}); err != nil {
			return fmt.Errorf(
				"owner %q step stamp version %d: %w",
				owner.Name,
				owner.Version,
				err,
			)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			ALTER TABLE %s OWNER TO memoh_migrate;
			REVOKE ALL ON TABLE %s FROM PUBLIC;
			GRANT SELECT ON TABLE %s TO memoh_%s;
			REVOKE INSERT, UPDATE, DELETE ON TABLE %s FROM memoh_%s`,
			owner.VersionTable,
			owner.VersionTable,
			owner.VersionTable,
			owner.Name,
			owner.VersionTable,
			owner.Name,
		)); err != nil {
			return fmt.Errorf("owner %q step secure version table: %w", owner.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("upgrade-v2 commit transaction: %w", err)
	}
	committed = true
	return nil
}

func loadBridgePlan(fsys fs.FS) (bridgePlan, error) {
	planPath := path.Join(bridgeRoot, "plan.yaml")
	data, err := fs.ReadFile(fsys, planPath)
	if err != nil {
		return bridgePlan{}, err
	}
	var plan bridgePlan
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&plan); err != nil {
		return bridgePlan{}, fmt.Errorf("parse plan: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return bridgePlan{}, errors.New("parse plan: multiple YAML documents are not allowed")
		}
		return bridgePlan{}, fmt.Errorf("parse plan: %w", err)
	}
	if plan.FromEpoch != 1 || plan.ToEpoch != CurrentEpoch {
		return bridgePlan{}, fmt.Errorf(
			"plan epoch transition must be 1 -> %d",
			CurrentEpoch,
		)
	}
	if plan.Requires.SchemaMigrationsVersion != 119 || plan.Requires.Dirty {
		return bridgePlan{}, errors.New("plan requires public.schema_migrations version 119 and dirty=false")
	}
	if len(plan.Steps) != len(bridgeStepIDs) {
		return bridgePlan{}, fmt.Errorf("plan must contain exactly %d steps", len(bridgeStepIDs))
	}
	for i, step := range plan.Steps {
		if step.ID != bridgeStepIDs[i] {
			return bridgePlan{}, fmt.Errorf("plan step %02d id is %q, want %q", i+1, step.ID, bridgeStepIDs[i])
		}
		if !fs.ValidPath(step.SQL) || path.Dir(step.SQL) != "sql" {
			return bridgePlan{}, fmt.Errorf("plan step %02d has unsafe SQL path %q", i+1, step.SQL)
		}
		wantPrefix := fmt.Sprintf("%02d_", i+1)
		if !strings.HasPrefix(path.Base(step.SQL), wantPrefix) {
			return bridgePlan{}, fmt.Errorf("plan step %02d SQL path must start with %q", i+1, wantPrefix)
		}
		if _, err := fs.Stat(fsys, path.Join(bridgeRoot, step.SQL)); err != nil {
			return bridgePlan{}, fmt.Errorf("plan step %02d SQL asset: %w", i+1, err)
		}
		if i == 2 {
			if !fs.ValidPath(step.CrossOwnerFKList) || path.Dir(step.CrossOwnerFKList) != "." {
				return bridgePlan{}, fmt.Errorf(
					"plan step %02d has unsafe cross_owner_fk_list %q",
					i+1,
					step.CrossOwnerFKList,
				)
			}
			if _, err := fs.Stat(fsys, path.Join(bridgeRoot, step.CrossOwnerFKList)); err != nil {
				return bridgePlan{}, fmt.Errorf("plan step %02d cross_owner_fk_list asset: %w", i+1, err)
			}
		} else if step.CrossOwnerFKList != "" {
			return bridgePlan{}, fmt.Errorf("plan step %02d cannot declare cross_owner_fk_list", i+1)
		}
		if i != len(plan.Steps)-1 && step.StampOwnersTo != 0 {
			return bridgePlan{}, fmt.Errorf("plan step %02d cannot declare stamp_owners_to", i+1)
		}
	}
	last := plan.Steps[len(plan.Steps)-1]
	if last.StampOwnersTo != 1 {
		return bridgePlan{}, fmt.Errorf("plan stamp_owners_to is %d, want 1", last.StampOwnersTo)
	}
	return plan, nil
}

func validateBridgeSource(state State, legacy LegacyStatus, plan bridgePlan) error {
	if state == StatePartial {
		return errors.New("partial Epoch v2 state is not bridgeable")
	}
	if state == StateEmpty {
		return errors.New("empty database must use migrate up")
	}
	if state != StateV1 || !legacy.Exists {
		return fmt.Errorf("database state %q is not a v1 source", state)
	}
	if legacy.Version != plan.Requires.SchemaMigrationsVersion {
		return fmt.Errorf(
			"public.schema_migrations version is %d, want %d",
			legacy.Version,
			plan.Requires.SchemaMigrationsVersion,
		)
	}
	if legacy.Dirty != plan.Requires.Dirty {
		return fmt.Errorf(
			"public.schema_migrations dirty is %t, want %t",
			legacy.Dirty,
			plan.Requires.Dirty,
		)
	}
	return nil
}
