package db_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	dbembed "github.com/memohai/memoh/db"
)

var ownerOrder = []string{
	"iam", "api", "model", "media", "agent", "channel", "memory", "runtime",
}

// Corrected Epoch v2 owner map (58 tables, no duplicates).
var ownerTables = map[string][]string{
	"iam": {"teams", "users", "team_members"},
	"api": {
		"bots", "bot_user_grants", "bot_acl_rules", "bot_channel_admins",
		"channel_link_codes", "user_channel_identity_bindings", "user_channel_bindings",
	},
	"agent": {
		"bot_heartbeat_logs", "bot_history_message_assets", "bot_history_message_compacts",
		"bot_history_messages", "bot_plugin_installations", "bot_plugin_resources", "bot_sessions",
		"mcp_connections", "mcp_oauth_tokens", "schedule", "schedule_logs", "subagent_configs",
		"tool_approval_requests", "user_input_requests",
	},
	"channel": {
		"bot_channel_configs", "bot_channel_routes", "bot_email_bindings", "bot_session_discuss_cursors",
		"bot_session_events", "channel_identities", "email_oauth_tokens", "email_outbox", "email_providers",
	},
	"memory":  {"memory_edges", "memory_nodes", "memory_providers"},
	"runtime": {"bot_remote_runtime_bindings", "bot_workspace_resource_limits", "container_versions", "containers", "lifecycle_events", "snapshots", "user_runtimes", "tasks"},
	"model": {
		"fetch_providers", "model_variants", "models", "provider_oauth_tokens", "providers",
		"search_providers", "tts_models", "tts_providers", "user_provider_oauth_tokens",
		"provider_templates", "provider_template_models",
	},
	"media": {"bot_storage_bindings", "media_assets", "storage_providers"},
}

type manifestFile struct {
	Path     string `yaml:"path"`
	Version  int    `yaml:"version"`
	Checksum string `yaml:"checksum"`
}

type manifestOwner struct {
	Name          string         `yaml:"name"`
	Schema        string         `yaml:"schema"`
	VersionTable  string         `yaml:"version_table"`
	MigrationsDir string         `yaml:"migrations_dir"`
	Version       int            `yaml:"version"`
	Dependencies  []string       `yaml:"dependencies"`
	Files         []manifestFile `yaml:"files"`
}

type manifest struct {
	Epoch  int             `yaml:"epoch"`
	Order  []string        `yaml:"order"`
	Owners []manifestOwner `yaml:"owners"`
}

type crossOwnerFK struct {
	Constraint string `json:"constraint"`
	Src        string `json:"src"`
	SrcOwner   string `json:"src_owner"`
	Dst        string `json:"dst"`
	DstOwner   string `json:"dst_owner"`
}

func TestLegacyMigrationsArchivedWithFrozenChecksums(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat(filepath.Join("postgres", "migrations")); !os.IsNotExist(err) {
		t.Fatalf("active path db/postgres/migrations must not exist after archive; err=%v", err)
	}

	checksumFile, err := dbembed.MigrationsFS.ReadFile("postgres/legacy/v1/migrations.sha256")
	if err != nil {
		t.Fatalf("read legacy checksum ledger: %v", err)
	}
	want := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(checksumFile)), "\n") {
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			t.Fatalf("invalid checksum line: %q", line)
		}
		want[parts[1]] = parts[0]
	}
	if len(want) == 0 {
		t.Fatal("legacy checksum ledger is empty")
	}

	err = fs.WalkDir(dbembed.MigrationsFS, "postgres/legacy/v1/migrations", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		base := path.Base(name)
		data, err := dbembed.MigrationsFS.ReadFile(name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		expected, ok := want[base]
		if !ok {
			t.Errorf("unexpected legacy file %s", base)
			return nil
		}
		if got != expected {
			t.Errorf("checksum mismatch for %s", base)
		}
		delete(want, base)
		return nil
	})
	if err != nil {
		t.Fatalf("walk legacy migrations: %v", err)
	}
	if len(want) != 0 {
		t.Fatalf("missing legacy files in embed: %d", len(want))
	}
}

func TestActiveTreeHasNoLegacyFlatMigrationsPath(t *testing.T) {
	t.Parallel()
	entries, err := fs.ReadDir(dbembed.MigrationsFS, "postgres")
	if err != nil {
		t.Fatalf("read postgres embed: %v", err)
	}
	for _, e := range entries {
		if e.Name() == "migrations" {
			t.Fatal("embed still contains postgres/migrations active path")
		}
	}
}

func TestManifestParsesAndChecksumsMatch(t *testing.T) {
	t.Parallel()
	raw, err := dbembed.MigrationsFS.ReadFile("postgres/manifest.yaml")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if m.Epoch != 2 {
		t.Fatalf("epoch = %d, want 2", m.Epoch)
	}
	if strings.Join(m.Order, ",") != strings.Join(ownerOrder, ",") {
		t.Fatalf("order = %v, want %v", m.Order, ownerOrder)
	}
	if len(m.Owners) != len(ownerOrder) {
		t.Fatalf("owners = %d, want %d", len(m.Owners), len(ownerOrder))
	}
	// Collect every owner's violations instead of failing on the first. A
	// stale manifest usually goes stale for all owners at once, and a Fatalf
	// here reports one of them as if it were the only one.
	var violations []string
	for i, owner := range m.Owners {
		fail := func(format string, args ...any) {
			violations = append(violations, fmt.Sprintf("owner %s: ", owner.Name)+fmt.Sprintf(format, args...))
		}
		if owner.Name != ownerOrder[i] {
			fail("position %d, want %s", i, ownerOrder[i])
			continue
		}
		if owner.Schema != owner.Name || owner.VersionTable != owner.Name+".goose_db_version" {
			fail("schema/version_table mismatch: %+v", owner)
		}
		if owner.Version != 1 || len(owner.Files) != 1 {
			fail("version/files mismatch: %+v", owner)
			continue
		}
		file := owner.Files[0]
		if !strings.HasPrefix(file.Checksum, "sha256:") {
			fail("checksum missing sha256 prefix: %s", file.Checksum)
		}
		data, err := dbembed.MigrationsFS.ReadFile("postgres/" + file.Path)
		if err != nil {
			fail("read %s: %v", file.Path, err)
			continue
		}
		sum := sha256.Sum256(data)
		want := "sha256:" + hex.EncodeToString(sum[:])
		if file.Checksum != want {
			fail("checksum mismatch for %s\n      manifest %s\n      actual   %s", file.Path, file.Checksum, want)
		}
		text := string(data)
		if !strings.Contains(text, "-- +goose Up") {
			fail("%s missing goose Up", file.Path)
		}
		if strings.Contains(text, "CREATE TABLE public.") {
			fail("%s creates public business tables", file.Path)
		}
		if strings.Contains(text, `\restrict`) {
			fail("%s contains dump restrict noise", file.Path)
		}
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, "OWNER TO") && !strings.Contains(line, "OWNER TO memoh_migrate") {
				fail("%s has non-migrate OWNER TO: %s", file.Path, line)
			}
		}
		if !strings.Contains(text, "OWNER TO memoh_migrate") {
			fail("%s missing OWNER TO memoh_migrate privilege contract", file.Path)
		}
		if !strings.Contains(text, owner.Name+".goose_db_version") {
			fail("%s missing goose_db_version privilege handling", file.Path)
		}
	}
	if len(violations) > 0 {
		t.Fatalf("manifest does not describe the published migrations (%d owner(s)); run `mise run db-manifest-checksums`:\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}

func TestGooseBaselineStructureExactlyOneUpDown(t *testing.T) {
	t.Parallel()
	upRe := regexp.MustCompile(`(?m)^-- \+goose Up\s*$`)
	downRe := regexp.MustCompile(`(?m)^-- \+goose Down\s*$`)
	beginRe := regexp.MustCompile(`(?m)^-- \+goose StatementBegin\s*$`)
	endRe := regexp.MustCompile(`(?m)^-- \+goose StatementEnd\s*$`)
	for _, owner := range ownerOrder {
		data, err := dbembed.MigrationsFS.ReadFile("postgres/" + owner + "/migrations/00001_baseline.sql")
		if err != nil {
			t.Fatalf("read %s: %v", owner, err)
		}
		text := string(data)
		if got := len(upRe.FindAllString(text, -1)); got != 1 {
			t.Fatalf("%s: want 1 Up, got %d", owner, got)
		}
		if got := len(downRe.FindAllString(text, -1)); got != 1 {
			t.Fatalf("%s: want 1 Down, got %d", owner, got)
		}
		if got := len(beginRe.FindAllString(text, -1)); got != 2 {
			t.Fatalf("%s: want 2 StatementBegin, got %d", owner, got)
		}
		if got := len(endRe.FindAllString(text, -1)); got != 2 {
			t.Fatalf("%s: want 2 StatementEnd, got %d", owner, got)
		}
		upIdx := strings.Index(text, "-- +goose Up")
		downIdx := strings.Index(text, "-- +goose Down")
		footerIdx := strings.Index(text, "-- Explicit privilege contract")
		if footerIdx < 0 || footerIdx > downIdx || footerIdx < upIdx {
			t.Fatalf("%s: privilege footer must sit inside Up before Down", owner)
		}
		// Up body between first StatementBegin and first StatementEnd
		begin := strings.Index(text, "-- +goose StatementBegin")
		end := strings.Index(text, "-- +goose StatementEnd")
		upBody := text[begin:end]
		if !strings.Contains(upBody, "-- Explicit privilege contract") {
			t.Fatalf("%s: privilege footer outside Up StatementBegin/End", owner)
		}
		if owner == "iam" {
			setIdx := strings.Index(upBody, "SET LOCAL ROLE memoh_migrate;")
			typeIdx := strings.Index(upBody, "CREATE TYPE iam.user_role")
			if setIdx < 0 || typeIdx < 0 || setIdx > typeIdx {
				t.Fatalf("iam must SET LOCAL ROLE memoh_migrate before type/table DDL")
			}
			ownerIdx := strings.Index(upBody, "ALTER TABLE iam.goose_db_version OWNER TO memoh_migrate")
			if ownerIdx < 0 {
				t.Fatal("iam must transfer goose_db_version ownership before SET LOCAL ROLE")
			}
			if ownerIdx > setIdx {
				t.Fatal("iam goose_db_version OWNER transfer must precede SET LOCAL ROLE")
			}
			continue
		}
		if !strings.Contains(upBody, "RESET ROLE;") {
			t.Fatalf("%s must RESET ROLE before taking goose_db_version ownership", owner)
		}
		resetIdx := strings.Index(upBody, "RESET ROLE;")
		ownerIdx := strings.Index(upBody, fmt.Sprintf("ALTER TABLE %s.goose_db_version OWNER TO memoh_migrate", owner))
		setIdx := strings.Index(upBody, "SET LOCAL ROLE memoh_migrate;")
		if ownerIdx < 0 || setIdx < 0 || resetIdx < 0 {
			t.Fatalf("%s missing RESET/OWNER/SET LOCAL ROLE sequence for goose_db_version", owner)
		}
		if resetIdx >= ownerIdx || ownerIdx >= setIdx {
			t.Fatalf("%s must RESET ROLE, transfer goose_db_version OWNER, then SET LOCAL ROLE", owner)
		}
	}
}

func TestOwnerBaselinesOwnOnlyTheirSchemas(t *testing.T) {
	t.Parallel()
	schemaContractRe := regexp.MustCompile(`(?m)(?:CREATE SCHEMA IF NOT EXISTS|ALTER SCHEMA|ON SCHEMA|IN SCHEMA) ([a-z]+)`)
	for _, owner := range ownerOrder {
		data, err := dbembed.MigrationsFS.ReadFile("postgres/" + owner + "/migrations/00001_baseline.sql")
		if err != nil {
			t.Fatalf("read %s baseline: %v", owner, err)
		}
		text := string(data)
		for _, needle := range []string{
			"CREATE SCHEMA IF NOT EXISTS " + owner + ";",
			"ALTER SCHEMA " + owner + " OWNER TO memoh_migrate;",
			"REVOKE ALL ON SCHEMA " + owner + " FROM PUBLIC;",
			"GRANT USAGE, CREATE ON SCHEMA " + owner + " TO memoh_migrate;",
			"GRANT USAGE ON SCHEMA " + owner + " TO memoh_" + owner + ";",
			"ALTER DEFAULT PRIVILEGES FOR ROLE memoh_migrate IN SCHEMA " + owner,
		} {
			if !strings.Contains(text, needle) {
				t.Errorf("%s baseline missing owner-local schema contract %q", owner, needle)
			}
		}
		for _, match := range schemaContractRe.FindAllStringSubmatch(text, -1) {
			if match[1] != owner {
				t.Errorf("%s baseline configures schema %q", owner, match[1])
			}
		}
	}
}

func TestBaselinesMatchOwnerMapAndPlatformSeed(t *testing.T) {
	t.Parallel()
	tableRe := regexp.MustCompile(`(?m)^CREATE TABLE ([a-z]+)\.([a-z0-9_]+)`)
	for owner, wantTables := range ownerTables {
		data, err := dbembed.MigrationsFS.ReadFile("postgres/" + owner + "/migrations/00001_baseline.sql")
		if err != nil {
			t.Fatalf("read %s baseline: %v", owner, err)
		}
		text := string(data)
		got := map[string]bool{}
		for _, m := range tableRe.FindAllStringSubmatch(text, -1) {
			if m[1] != owner {
				t.Fatalf("%s baseline creates %s.%s", owner, m[1], m[2])
			}
			if m[2] == "goose_db_version" {
				t.Fatalf("%s baseline must not create goose_db_version", owner)
			}
			got[m[2]] = true
		}
		if len(got) != len(wantTables) {
			t.Fatalf("%s baseline tables=%d want=%d got=%v", owner, len(got), len(wantTables), keys(got))
		}
		for _, table := range wantTables {
			if !got[table] {
				t.Fatalf("%s baseline missing table %s", owner, table)
			}
		}
	}

	iam, err := dbembed.MigrationsFS.ReadFile("postgres/iam/migrations/00001_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	pt := string(iam)
	for _, needle := range []string{
		"CREATE TYPE iam.user_role",
		"CREATE FUNCTION iam.memoh_current_team_id",
		"CREATE FUNCTION iam.memoh_guard_last_active_team_admin",
		"CREATE TRIGGER team_members_last_active_admin_guard",
		"INSERT INTO iam.teams",
		"00000000-0000-0000-0000-000000000001",
		"'default'",
		"CREATE ROLE memoh_migrate NOLOGIN",
		"GRANT memoh_migrate, memoh_iam, memoh_api, memoh_agent, memoh_channel, memoh_memory, memoh_runtime, memoh_model, memoh_media TO %I",
		"SET LOCAL ROLE memoh_migrate",
		"ALTER SCHEMA iam OWNER TO memoh_migrate",
		"ALTER ROLE memoh_api SET search_path TO api, iam, public",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA iam",
	} {
		if !strings.Contains(pt, needle) {
			t.Fatalf("iam baseline missing %q", needle)
		}
	}
	if strings.Contains(pt, "FROM api.users") || strings.Contains(pt, "FROM public.users") {
		t.Fatal("iam guard body must qualify iam tables only")
	}
	if !strings.Contains(pt, "FROM iam.users") || !strings.Contains(pt, "FROM iam.team_members") {
		t.Fatal("iam guard body must reference iam.users/team_members")
	}

	api, err := dbembed.MigrationsFS.ReadFile("postgres/api/migrations/00001_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(api), "INSERT INTO api.teams") || strings.Contains(string(api), "CREATE TABLE api.teams") {
		t.Fatal("api baseline must not own teams/default team seed")
	}
}

func TestCrossOwnerFKDropListIsNonEmptyAndSelfConsistent(t *testing.T) {
	t.Parallel()
	raw, err := dbembed.MigrationsFS.ReadFile("postgres/legacy/v1/upgrade/to_v2/cross_owner_fks.json")
	if err != nil {
		t.Fatalf("read cross_owner_fks.json: %v", err)
	}
	var drops []crossOwnerFK
	if err := json.Unmarshal(raw, &drops); err != nil {
		t.Fatalf("parse cross_owner_fks.json: %v", err)
	}
	if len(drops) == 0 {
		t.Fatal("expected non-empty cross-owner FK drop list")
	}
	dropSQL, err := dbembed.MigrationsFS.ReadFile("postgres/legacy/v1/upgrade/to_v2/sql/03_drop_cross_owner_fks.sql")
	if err != nil {
		t.Fatal(err)
	}
	sqlText := string(dropSQL)
	ownerOf := map[string]string{}
	for owner, tables := range ownerTables {
		for _, table := range tables {
			ownerOf[table] = owner
		}
	}
	for _, fk := range drops {
		if fk.SrcOwner == fk.DstOwner {
			t.Fatalf("drop list contains same-owner FK %s", fk.Constraint)
		}
		if ownerOf[fk.Src] != fk.SrcOwner || ownerOf[fk.Dst] != fk.DstOwner {
			t.Fatalf("drop list owner mismatch for %s: src=%s/%s dst=%s/%s", fk.Constraint, fk.Src, fk.SrcOwner, fk.Dst, fk.DstOwner)
		}
		if !strings.Contains(sqlText, "DROP CONSTRAINT IF EXISTS "+fk.Constraint) {
			t.Fatalf("drop SQL missing constraint %s", fk.Constraint)
		}
	}
}

func TestUpgradePlanStepsAreExplicit(t *testing.T) {
	t.Parallel()
	raw, err := dbembed.MigrationsFS.ReadFile("postgres/legacy/v1/upgrade/to_v2/plan.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, step := range []string{
		"preflight", "create_schemas", "drop_cross_owner_fks", "move_types_functions_sequences",
		"move_tables", "fix_platform_functions", "verify", "stamp_ready",
	} {
		if !strings.Contains(text, "id: "+step) {
			t.Fatalf("plan missing step %s", step)
		}
	}
	if strings.Contains(text, "fix_api_guard_function") {
		t.Fatal("plan still references obsolete api guard step")
	}
}

func TestAgentMessageQueriesUseOwnerLocalSnapshots(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("postgres", "agent", "queries", "messages.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, placeholder := range []string{
		"NULL::text AS sender_display_name",
		"NULL::text AS sender_avatar_url",
		"NULL::text AS conversation_type",
		"NULL::text AS conversation_name",
		"NULL::text AS reply_target",
	} {
		if strings.Contains(text, placeholder) {
			t.Fatalf("message queries contain sender snapshot placeholder %q", placeholder)
		}
	}
	const insert = "INSERT INTO agent.bot_history_messages ("
	parts := strings.Split(text, insert)
	if got := len(parts) - 1; got != 6 {
		t.Fatalf("message insert count = %d, want 6", got)
	}
	for i, part := range parts[1:] {
		columns, _, ok := strings.Cut(part, ")")
		if !ok {
			t.Fatalf("message insert %d has no column list", i+1)
		}
		for _, column := range []string{"sender_display_name", "sender_avatar_url"} {
			if !strings.Contains(columns, column) {
				t.Fatalf("message insert %d missing column %s", i+1, column)
			}
		}
	}
}

func TestAgentSnapshotSchemaAndBridge(t *testing.T) {
	t.Parallel()
	baseline, err := dbembed.MigrationsFS.ReadFile("postgres/agent/migrations/00001_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{
		"sender_display_name text",
		"sender_avatar_url text",
		"conversation_type text",
		"conversation_name text",
		"reply_target text",
	} {
		if !strings.Contains(string(baseline), column) {
			t.Fatalf("agent baseline missing %q", column)
		}
	}

	bridge, err := dbembed.MigrationsFS.ReadFile("postgres/legacy/v1/upgrade/to_v2/sql/05_move_tables.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(bridge)
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS sender_display_name text",
		"ADD COLUMN IF NOT EXISTS sender_avatar_url text",
		"ADD COLUMN IF NOT EXISTS conversation_type text",
		"ADD COLUMN IF NOT EXISTS conversation_name text",
		"ADD COLUMN IF NOT EXISTS reply_target text",
		"UPDATE agent.bot_history_messages AS message",
		"UPDATE agent.bot_sessions AS session",
		"FROM channel.bot_channel_routes AS route",
		"route.metadata->>'conversation_name'",
		"channel.channel_identities",
		"iam.users",
		"COALESCE(",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("bridge sender snapshot contract missing %q", fragment)
		}
	}
}

func TestBridgeStep06OwnershipAndGrants(t *testing.T) {
	t.Parallel()
	raw, err := dbembed.MigrationsFS.ReadFile("postgres/legacy/v1/upgrade/to_v2/sql/06_fix_platform_functions.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	required := []string{
		"OWNER TO memoh_migrate",
		"ALTER TYPE iam.user_role OWNER TO memoh_migrate",
		"SET LOCAL ROLE memoh_migrate",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA",
		"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA",
		"GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA",
		"REVOKE CREATE ON SCHEMA",
		"GRANT EXECUTE ON FUNCTION iam.memoh_current_team_id()",
		"GRANT EXECUTE ON FUNCTION iam.memoh_guard_last_active_team_admin() TO memoh_iam, memoh_migrate",
		"public.schema_migrations",
		"format(",
	}
	for _, needle := range required {
		if !strings.Contains(text, needle) {
			t.Fatalf("step 06 missing ownership/grants contract fragment: %q", needle)
		}
	}
	if !strings.Contains(text, "CREATE OR REPLACE FUNCTION iam.memoh_current_team_id()") {
		t.Fatal("step 06 must still rewrite iam.memoh_current_team_id")
	}
	if !strings.Contains(text, "CREATE OR REPLACE FUNCTION iam.memoh_guard_last_active_team_admin()") {
		t.Fatal("step 06 must still rewrite iam.memoh_guard_last_active_team_admin")
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
