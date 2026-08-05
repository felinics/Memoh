package botbackup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/db"
	dbstore "github.com/memohai/memoh/internal/db/store"
)

// fakeProjectStore is the slice of dbstore.BotProjectStore restoreProjects
// uses, with live-path uniqueness modeled the way the DB's partial index
// behaves: archived rows do not occupy a path.
type fakeProjectStore struct {
	records []dbstore.BotProjectRecord
	nextID  int
}

func (s *fakeProjectStore) CreateProject(_ context.Context, input dbstore.CreateBotProjectInput) (dbstore.BotProjectRecord, error) {
	for _, record := range s.records {
		if record.Path == input.Path && record.ArchivedAt.IsZero() {
			return dbstore.BotProjectRecord{}, errors.New("duplicate live project path")
		}
	}
	s.nextID++
	record := dbstore.BotProjectRecord{
		ID:         fmt.Sprintf("00000000-0000-0000-0000-%012d", s.nextID),
		BotID:      input.BotID,
		Name:       input.Name,
		TargetKind: input.TargetKind,
		Path:       input.Path,
	}
	s.records = append(s.records, record)
	return record, nil
}

func (s *fakeProjectStore) ListProjects(_ context.Context, _ string, includeArchived bool) ([]dbstore.BotProjectRecord, error) {
	out := make([]dbstore.BotProjectRecord, 0, len(s.records))
	for _, record := range s.records {
		if !includeArchived && !record.ArchivedAt.IsZero() {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

func (s *fakeProjectStore) GetProject(_ context.Context, _, projectID string) (dbstore.BotProjectRecord, error) {
	for _, record := range s.records {
		if record.ID == projectID {
			return record, nil
		}
	}
	return dbstore.BotProjectRecord{}, db.ErrNotFound
}

func (s *fakeProjectStore) RenameProject(_ context.Context, _, projectID, name string) (dbstore.BotProjectRecord, error) {
	for i := range s.records {
		if s.records[i].ID == projectID {
			s.records[i].Name = name
			return s.records[i], nil
		}
	}
	return dbstore.BotProjectRecord{}, db.ErrNotFound
}

func (s *fakeProjectStore) ArchiveProject(_ context.Context, _, projectID string) error {
	for i := range s.records {
		if s.records[i].ID == projectID {
			s.records[i].ArchivedAt = time.Unix(1, 0).UTC()
			return nil
		}
	}
	return db.ErrNotFound
}

func newProjectImportState(t *testing.T, projects []backupProject) *importState {
	t.Helper()
	raw, err := json.Marshal(projects)
	if err != nil {
		t.Fatalf("marshal projects: %v", err)
	}
	return &importState{
		entries:    map[string]backupZipEntry{"bot/projects.json": {data: raw}},
		projectMap: map[string]pgtype.UUID{},
		counts:     map[Section]int{},
		createMode: true,
	}
}

// An archived project must not claim its directory in the live-path index:
// a later live project for the same directory would otherwise be treated as
// already restored, remapping its sessions onto the archived project and
// never recreating the live one.
func TestRestoreProjectsKeepsArchivedRestoresOutOfLivePathMap(t *testing.T) {
	t.Parallel()

	store := &fakeProjectStore{}
	svc := &Service{projects: store}
	state := newProjectImportState(t, []backupProject{
		{ID: "src-archived", Name: "old", Path: "/data/site", Archived: true},
		{ID: "src-live", Name: "current", Path: "/data/site"},
	})

	if err := svc.restoreProjects(context.Background(), "bot-1", "user-1", state); err != nil {
		t.Fatalf("restoreProjects() error = %v", err)
	}
	if len(store.records) != 2 {
		t.Fatalf("created %d projects, want the archived one and a fresh live one: %+v", len(store.records), store.records)
	}
	archivedID := state.projectMap["src-archived"]
	liveID := state.projectMap["src-live"]
	if !archivedID.Valid || !liveID.Valid {
		t.Fatalf("project map = %+v, want both entries mapped", state.projectMap)
	}
	if archivedID == liveID {
		t.Fatal("the live project was folded into the archived one — its sessions would resolve to an archived project")
	}
	live, err := svc.projects.GetProject(context.Background(), "bot-1", uuidText(liveID))
	if err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}
	if !live.ArchivedAt.IsZero() {
		t.Fatalf("restored live project is archived: %+v", live)
	}
	if live.Name != "current" {
		t.Fatalf("live project name = %q, want the live backup entry's name", live.Name)
	}
}

const existingProjectID = "11111111-1111-1111-1111-111111111111"

// Merge mode still reuses an existing LIVE project for the same directory
// rather than tripping the unique constraint.
func TestRestoreProjectsReusesExistingLiveProjectForSamePath(t *testing.T) {
	t.Parallel()

	store := &fakeProjectStore{records: []dbstore.BotProjectRecord{
		{ID: existingProjectID, BotID: "bot-1", Name: "existing", Path: "/data/site"},
	}}
	svc := &Service{projects: store}
	state := newProjectImportState(t, []backupProject{
		{ID: "src-live", Name: "current", Path: "/data/site"},
	})

	if err := svc.restoreProjects(context.Background(), "bot-1", "user-1", state); err != nil {
		t.Fatalf("restoreProjects() error = %v", err)
	}
	if len(store.records) != 1 {
		t.Fatalf("created %d projects, want the existing one reused: %+v", len(store.records), store.records)
	}
	if got := uuidText(state.projectMap["src-live"]); got != existingProjectID {
		t.Fatalf("mapped project = %q, want the existing live project %q", got, existingProjectID)
	}
}

func uuidText(id pgtype.UUID) string {
	value, err := id.Value()
	if err != nil || value == nil {
		return ""
	}
	text, _ := value.(string)
	return text
}
