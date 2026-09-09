package botbackup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// fakeWorkdirStore is the slice of dbstore.BotWorkdirStore restoreWorkdirs
// uses, with live-path uniqueness modeled the way the DB's partial index
// behaves: archived rows do not occupy a path.
type fakeWorkdirStore struct {
	records []dbstore.BotWorkdirRecord
	nextID  int
}

func (s *fakeWorkdirStore) CreateWorkdir(_ context.Context, input dbstore.CreateBotWorkdirInput) (dbstore.BotWorkdirRecord, error) {
	for _, record := range s.records {
		if record.Path == input.Path && record.ArchivedAt.IsZero() {
			return dbstore.BotWorkdirRecord{}, errors.New("duplicate live workdir path")
		}
	}
	s.nextID++
	record := dbstore.BotWorkdirRecord{
		ID:         fmt.Sprintf("00000000-0000-0000-0000-%012d", s.nextID),
		BotID:      input.BotID,
		Name:       input.Name,
		TargetKind: input.TargetKind,
		Path:       input.Path,
	}
	s.records = append(s.records, record)
	return record, nil
}

func (s *fakeWorkdirStore) ListWorkdirs(_ context.Context, _ string, includeArchived bool) ([]dbstore.BotWorkdirRecord, error) {
	out := make([]dbstore.BotWorkdirRecord, 0, len(s.records))
	for _, record := range s.records {
		if !includeArchived && !record.ArchivedAt.IsZero() {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

func (s *fakeWorkdirStore) GetWorkdir(_ context.Context, _, workdirID string) (dbstore.BotWorkdirRecord, error) {
	for _, record := range s.records {
		if record.ID == workdirID {
			return record, nil
		}
	}
	return dbstore.BotWorkdirRecord{}, db.ErrNotFound
}

func (s *fakeWorkdirStore) RenameWorkdir(_ context.Context, _, workdirID, name string) (dbstore.BotWorkdirRecord, error) {
	for i := range s.records {
		if s.records[i].ID == workdirID {
			s.records[i].Name = name
			return s.records[i], nil
		}
	}
	return dbstore.BotWorkdirRecord{}, db.ErrNotFound
}

func (s *fakeWorkdirStore) ArchiveWorkdir(_ context.Context, _, workdirID string) error {
	for i := range s.records {
		if s.records[i].ID == workdirID {
			s.records[i].ArchivedAt = time.Unix(1, 0).UTC()
			return nil
		}
	}
	return db.ErrNotFound
}

func newWorkdirImportState(t *testing.T, workdirs []backupWorkdir) *importState {
	t.Helper()
	raw, err := json.Marshal(workdirs)
	if err != nil {
		t.Fatalf("marshal workdirs: %v", err)
	}
	return &importState{
		entries:    map[string]backupZipEntry{"bot/workdirs.json": {data: raw}},
		workdirMap: map[string]pgtype.UUID{},
		counts:     map[Section]int{},
		createMode: true,
	}
}

// An archived workdir must not claim its directory in the live-path index:
// a later live workdir for the same directory would otherwise be treated as
// already restored, remapping its sessions onto the archived workdir and
// never recreating the live one.
func TestRestoreWorkdirsKeepsArchivedRestoresOutOfLivePathMap(t *testing.T) {
	t.Parallel()

	store := &fakeWorkdirStore{}
	svc := &Service{workdirs: store}
	state := newWorkdirImportState(t, []backupWorkdir{
		{ID: "src-archived", Name: "old", Path: "/data/site", Archived: true},
		{ID: "src-live", Name: "current", Path: "/data/site"},
	})

	if err := svc.restoreWorkdirs(context.Background(), "bot-1", "user-1", state); err != nil {
		t.Fatalf("restoreWorkdirs() error = %v", err)
	}
	if len(store.records) != 2 {
		t.Fatalf("created %d workdirs, want the archived one and a fresh live one: %+v", len(store.records), store.records)
	}
	archivedID := state.workdirMap["src-archived"]
	liveID := state.workdirMap["src-live"]
	if !archivedID.Valid || !liveID.Valid {
		t.Fatalf("workdir map = %+v, want both entries mapped", state.workdirMap)
	}
	if archivedID == liveID {
		t.Fatal("the live workdir was folded into the archived one — its sessions would resolve to an archived workdir")
	}
	live, err := svc.workdirs.GetWorkdir(context.Background(), "bot-1", uuidText(liveID))
	if err != nil {
		t.Fatalf("GetWorkdir() error = %v", err)
	}
	if !live.ArchivedAt.IsZero() {
		t.Fatalf("restored live workdir is archived: %+v", live)
	}
	if live.Name != "current" {
		t.Fatalf("live workdir name = %q, want the live backup entry's name", live.Name)
	}
}

const existingWorkdirID = "11111111-1111-1111-1111-111111111111"

// Merge mode still reuses an existing LIVE workdir for the same directory
// rather than tripping the unique constraint.
func TestRestoreWorkdirsReusesExistingLiveWorkdirForSamePath(t *testing.T) {
	t.Parallel()

	store := &fakeWorkdirStore{records: []dbstore.BotWorkdirRecord{
		{ID: existingWorkdirID, BotID: "bot-1", Name: "existing", Path: "/data/site"},
	}}
	svc := &Service{workdirs: store}
	state := newWorkdirImportState(t, []backupWorkdir{
		{ID: "src-live", Name: "current", Path: "/data/site"},
	})

	if err := svc.restoreWorkdirs(context.Background(), "bot-1", "user-1", state); err != nil {
		t.Fatalf("restoreWorkdirs() error = %v", err)
	}
	if len(store.records) != 1 {
		t.Fatalf("created %d workdirs, want the existing one reused: %+v", len(store.records), store.records)
	}
	if got := uuidText(state.workdirMap["src-live"]); got != existingWorkdirID {
		t.Fatalf("mapped workdir = %q, want the existing live workdir %q", got, existingWorkdirID)
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
