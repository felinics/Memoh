package epoch

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	dbembed "github.com/memohai/memoh/db"
)

func TestClassifyState(t *testing.T) {
	tests := []struct {
		name            string
		versionTables   int
		ownerSchemas    int
		publicRelations int
		legacy          bool
		want            State
	}{
		{name: "empty", want: StateEmpty},
		{name: "v1", legacy: true, want: StateV1},
		{name: "v2", versionTables: 8, legacy: true, want: StateV2},
		{name: "partial version tables", versionTables: 1, want: StatePartial},
		{name: "partial schemas", ownerSchemas: 1, want: StatePartial},
		{name: "untracked public objects", publicRelations: 1, want: StatePartial},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyState(8, tt.versionTables, tt.ownerSchemas, tt.publicRelations, tt.legacy)
			if got != tt.want {
				t.Fatalf("classifyState() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadBridgePlan(t *testing.T) {
	fsys := validBridgeFS()

	plan, err := loadBridgePlan(fsys)
	if err != nil {
		t.Fatalf("loadBridgePlan() error = %v", err)
	}
	if len(plan.Steps) != 8 || plan.Steps[7].StampOwnersTo != 1 {
		t.Fatalf("loadBridgePlan() = %#v", plan)
	}
}

func TestEmbeddedBridgePlan(t *testing.T) {
	postgres, err := fs.Sub(dbembed.MigrationsFS, "postgres")
	if err != nil {
		t.Fatalf("fs.Sub(postgres) error = %v", err)
	}
	if _, err := loadBridgePlan(postgres); err != nil {
		t.Fatalf("loadBridgePlan(embedded) error = %v", err)
	}
}

func TestLoadBridgePlanRejectsInvalidPlan(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string) string
	}{
		{
			name: "unknown field",
			mutate: func(plan string) string {
				return plan + "\nunknown: true\n"
			},
		},
		{
			name: "wrong order",
			mutate: func(plan string) string {
				return strings.Replace(plan, "sql/01_preflight.sql", "sql/02_preflight.sql", 1)
			},
		},
		{
			name: "unsafe path",
			mutate: func(plan string) string {
				return strings.Replace(plan, "sql/01_preflight.sql", "../01_preflight.sql", 1)
			},
		},
		{
			name: "wrong source version",
			mutate: func(plan string) string {
				return strings.Replace(plan, fmt.Sprintf("schema_migrations_version: %d", finalV1SchemaVersion), "schema_migrations_version: 118", 1)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := validBridgeFS()
			plan := string(fsys[bridgeRoot+"/plan.yaml"].Data)
			fsys[bridgeRoot+"/plan.yaml"].Data = []byte(tt.mutate(plan))
			if _, err := loadBridgePlan(fsys); err == nil {
				t.Fatal("loadBridgePlan() error = nil, want error")
			}
		})
	}
}

func TestValidateBridgeSource(t *testing.T) {
	plan := bridgePlan{
		Requires: bridgeRequires{
			SchemaMigrationsVersion: finalV1SchemaVersion,
			Dirty:                   false,
		},
	}
	tests := []struct {
		name   string
		state  State
		legacy LegacyStatus
		wantOK bool
	}{
		{
			name:   "approved v1",
			state:  StateV1,
			legacy: LegacyStatus{Exists: true, Version: finalV1SchemaVersion},
			wantOK: true,
		},
		{
			name:   "wrong version",
			state:  StateV1,
			legacy: LegacyStatus{Exists: true, Version: 118},
		},
		{
			name:   "dirty",
			state:  StateV1,
			legacy: LegacyStatus{Exists: true, Version: finalV1SchemaVersion, Dirty: true},
		},
		{name: "empty", state: StateEmpty},
		{name: "partial", state: StatePartial},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBridgeSource(tt.state, tt.legacy, plan)
			if (err == nil) != tt.wantOK {
				t.Fatalf("validateBridgeSource() error = %v, wantOK %t", err, tt.wantOK)
			}
		})
	}
}

func validBridgeFS() fstest.MapFS {
	fsys := fstest.MapFS{
		bridgeRoot + "/cross_owner_fks.json": &fstest.MapFile{Data: []byte("[]")},
	}
	var steps strings.Builder
	for i, id := range bridgeStepIDs {
		number := i + 1
		sqlPath := "sql/" + twoDigits(number) + "_" + id + ".sql"
		fsys[bridgeRoot+"/"+sqlPath] = &fstest.MapFile{Data: []byte("SELECT 1;")}
		steps.WriteString("  - id: " + id + "\n")
		steps.WriteString("    sql: " + sqlPath + "\n")
		if i == 2 {
			steps.WriteString("    cross_owner_fk_list: cross_owner_fks.json\n")
		}
		if i == len(bridgeStepIDs)-1 {
			steps.WriteString("    stamp_owners_to: 1\n")
		}
	}
	plan := "from_epoch: 1\n" +
		"to_epoch: 2\n" +
		"requires:\n" +
		fmt.Sprintf("  schema_migrations_version: %d\n", finalV1SchemaVersion) +
		"  dirty: false\n" +
		"steps:\n" +
		steps.String()
	fsys[bridgeRoot+"/plan.yaml"] = &fstest.MapFile{Data: []byte(plan)}
	return fsys
}

func twoDigits(n int) string {
	return fmt.Sprintf("%02d", n)
}
