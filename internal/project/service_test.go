package project

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
)

// fakeQueries embeds the broad store interface; only the methods a test
// exercises are overridden, anything else panics loudly.
type fakeQueries struct {
	dbstore.Queries

	latestVersion    dbsqlc.ProjectNodeVersion
	latestVersionErr error
	renumbered       []dbsqlc.RenumberProjectNodeVersionParams
	inserted         []dbsqlc.InsertProjectNodeVersionParams

	parents map[string]dbsqlc.GetProjectNodeParentRow

	activities []dbsqlc.InsertProjectIssueActivityParams
}

func (f *fakeQueries) GetLatestProjectNodeVersion(_ context.Context, _ pgtype.UUID) (dbsqlc.ProjectNodeVersion, error) {
	return f.latestVersion, f.latestVersionErr
}

func (f *fakeQueries) RenumberProjectNodeVersion(_ context.Context, arg dbsqlc.RenumberProjectNodeVersionParams) (int64, error) {
	f.renumbered = append(f.renumbered, arg)
	return 1, nil
}

func (f *fakeQueries) InsertProjectNodeVersion(_ context.Context, arg dbsqlc.InsertProjectNodeVersionParams) error {
	f.inserted = append(f.inserted, arg)
	return nil
}

func (f *fakeQueries) GetProjectNodeParent(_ context.Context, nodeID pgtype.UUID) (dbsqlc.GetProjectNodeParentRow, error) {
	row, ok := f.parents[nodeID.String()]
	if !ok {
		return dbsqlc.GetProjectNodeParentRow{}, pgx.ErrNoRows
	}
	return row, nil
}

func (f *fakeQueries) InsertProjectIssueActivity(_ context.Context, arg dbsqlc.InsertProjectIssueActivityParams) error {
	f.activities = append(f.activities, arg)
	return nil
}

func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	id, err := db.ParseUUID(s)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", s, err)
	}
	return id
}

const (
	uuidA = "11111111-1111-1111-1111-111111111111"
	uuidB = "22222222-2222-2222-2222-222222222222"
	uuidC = "33333333-3333-3333-3333-333333333333"
	uuidD = "44444444-4444-4444-4444-444444444444"
)

func newTestService(q dbstore.Queries) *Service {
	s := NewService(nil, nil, q)
	s.now = func() time.Time { return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC) }
	return s
}

func pgTime(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func TestLandSnapshotMergesWithinWindow(t *testing.T) {
	editor := pgtype.UUID{}
	_ = editor.Scan(uuidA)
	fq := &fakeQueries{
		latestVersion: dbsqlc.ProjectNodeVersion{
			NodeID:       pgtype.UUID{},
			Version:      7,
			EditorUserID: editor,
			UpdatedAt:    pgTime(time.Date(2026, 8, 6, 11, 58, 0, 0, time.UTC)), // 2 min ago
		},
	}
	s := newTestService(fq)
	node := dbsqlc.ProjectNode{Version: 8, Title: "t", Body: "b"}

	if err := s.landSnapshot(context.Background(), fq, node, 7, editor); err != nil {
		t.Fatalf("landSnapshot: %v", err)
	}
	if len(fq.renumbered) != 1 || len(fq.inserted) != 0 {
		t.Fatalf("expected merge, got renumbered=%d inserted=%d", len(fq.renumbered), len(fq.inserted))
	}
	if fq.renumbered[0].OldVersion != 7 || fq.renumbered[0].NewVersion != 8 {
		t.Fatalf("renumber 7→8 expected, got %d→%d", fq.renumbered[0].OldVersion, fq.renumbered[0].NewVersion)
	}
}

func TestLandSnapshotNewRowWhenWindowClosed(t *testing.T) {
	editor := pgtype.UUID{}
	_ = editor.Scan(uuidA)
	fq := &fakeQueries{
		latestVersion: dbsqlc.ProjectNodeVersion{
			Version:      7,
			EditorUserID: editor,
			UpdatedAt:    pgTime(time.Date(2026, 8, 6, 11, 40, 0, 0, time.UTC)), // 20 min ago
		},
	}
	s := newTestService(fq)
	node := dbsqlc.ProjectNode{Version: 8, Title: "t", Body: "b"}

	if err := s.landSnapshot(context.Background(), fq, node, 7, editor); err != nil {
		t.Fatalf("landSnapshot: %v", err)
	}
	if len(fq.renumbered) != 0 || len(fq.inserted) != 1 {
		t.Fatalf("expected insert, got renumbered=%d inserted=%d", len(fq.renumbered), len(fq.inserted))
	}
	if fq.inserted[0].Version != 8 {
		t.Fatalf("inserted version = %d, want 8", fq.inserted[0].Version)
	}
}

func TestLandSnapshotNewRowForDifferentEditor(t *testing.T) {
	editorA, editorB := pgtype.UUID{}, pgtype.UUID{}
	_ = editorA.Scan(uuidA)
	_ = editorB.Scan(uuidB)
	fq := &fakeQueries{
		latestVersion: dbsqlc.ProjectNodeVersion{
			Version:      7,
			EditorUserID: editorA,
			UpdatedAt:    pgTime(time.Date(2026, 8, 6, 11, 59, 0, 0, time.UTC)),
		},
	}
	s := newTestService(fq)
	node := dbsqlc.ProjectNode{Version: 8}

	if err := s.landSnapshot(context.Background(), fq, node, 7, editorB); err != nil {
		t.Fatalf("landSnapshot: %v", err)
	}
	if len(fq.renumbered) != 0 || len(fq.inserted) != 1 {
		t.Fatalf("different editor must open a new version, got renumbered=%d inserted=%d", len(fq.renumbered), len(fq.inserted))
	}
}

func TestLandSnapshotNewRowOnVersionSkew(t *testing.T) {
	editor := pgtype.UUID{}
	_ = editor.Scan(uuidA)
	fq := &fakeQueries{
		latestVersion: dbsqlc.ProjectNodeVersion{
			Version:      5, // latest snapshot lags the expected version
			EditorUserID: editor,
			UpdatedAt:    pgTime(time.Date(2026, 8, 6, 11, 59, 0, 0, time.UTC)),
		},
	}
	s := newTestService(fq)
	node := dbsqlc.ProjectNode{Version: 8}

	if err := s.landSnapshot(context.Background(), fq, node, 7, editor); err != nil {
		t.Fatalf("landSnapshot: %v", err)
	}
	if len(fq.renumbered) != 0 || len(fq.inserted) != 1 {
		t.Fatalf("version skew must append, got renumbered=%d inserted=%d", len(fq.renumbered), len(fq.inserted))
	}
}

func TestEnsureNoCycleDetectsLoop(t *testing.T) {
	// moving = A; candidate parent = C; chain C → B → A means cycle.
	fq := &fakeQueries{parents: map[string]dbsqlc.GetProjectNodeParentRow{}}
	a, b, c := mustUUID(t, uuidA), mustUUID(t, uuidB), mustUUID(t, uuidC)
	fq.parents[c.String()] = dbsqlc.GetProjectNodeParentRow{ID: c, ParentID: b}
	fq.parents[b.String()] = dbsqlc.GetProjectNodeParentRow{ID: b, ParentID: a}
	fq.parents[a.String()] = dbsqlc.GetProjectNodeParentRow{ID: a}
	s := newTestService(fq)

	err := s.ensureNoCycle(context.Background(), fq, uuidA, uuidC)
	if !errors.Is(err, ErrMoveCycle) {
		t.Fatalf("expected ErrMoveCycle, got %v", err)
	}
}

func TestEnsureNoCycleAllowsSiblingMove(t *testing.T) {
	// moving = A; candidate parent = D whose chain is D → B → root.
	fq := &fakeQueries{parents: map[string]dbsqlc.GetProjectNodeParentRow{}}
	b, d := mustUUID(t, uuidB), mustUUID(t, uuidD)
	fq.parents[d.String()] = dbsqlc.GetProjectNodeParentRow{ID: d, ParentID: b}
	fq.parents[b.String()] = dbsqlc.GetProjectNodeParentRow{ID: b}
	s := newTestService(fq)

	if err := s.ensureNoCycle(context.Background(), fq, uuidA, uuidD); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveIssueFieldsPartialSemantics(t *testing.T) {
	assignee := pgtype.UUID{}
	_ = assignee.Scan(uuidA)
	current := dbsqlc.ProjectIssueDetail{
		Status:         StatusTodo,
		AssigneeUserID: assignee,
		Priority:       pgtype.Text{String: PriorityHigh, Valid: true},
		DueAt:          pgTime(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)),
	}

	// nil everywhere keeps everything.
	fields, err := resolveIssueFields(current, UpdateIssueRequest{})
	if err != nil {
		t.Fatalf("resolveIssueFields: %v", err)
	}
	if fields.status != StatusTodo || !fields.assignee.Valid || !fields.priority.Valid || !fields.dueAt.Valid {
		t.Fatalf("nil fields must keep current values: %+v", fields)
	}

	// Explicit empties clear assignee/priority/due.
	empty := ""
	fields, err = resolveIssueFields(current, UpdateIssueRequest{
		AssigneeUserID: &empty,
		Priority:       &empty,
		DueAt:          &empty,
	})
	if err != nil {
		t.Fatalf("resolveIssueFields: %v", err)
	}
	if fields.assignee.Valid || fields.priority.Valid || fields.dueAt.Valid {
		t.Fatalf("empty strings must clear: %+v", fields)
	}

	// Setting a bot assignee displaces the user assignee.
	bot := uuidB
	fields, err = resolveIssueFields(current, UpdateIssueRequest{AssigneeBotID: &bot})
	if err != nil {
		t.Fatalf("resolveIssueFields: %v", err)
	}
	if fields.assignee.Valid || !fields.bot.Valid {
		t.Fatalf("bot assignee must displace user assignee: %+v", fields)
	}

	// Invalid enum values are rejected.
	bad := "nope"
	if _, err := resolveIssueFields(current, UpdateIssueRequest{Status: &bad}); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("expected ErrInvalidStatus, got %v", err)
	}
	if _, err := resolveIssueFields(current, UpdateIssueRequest{Priority: &bad}); !errors.Is(err, ErrInvalidPriority) {
		t.Fatalf("expected ErrInvalidPriority, got %v", err)
	}
}

func TestRecordIssueActivityDiffs(t *testing.T) {
	fq := &fakeQueries{}
	s := newTestService(fq)
	actor := pgtype.UUID{}
	_ = actor.Scan(uuidA)
	assignee := pgtype.UUID{}
	_ = assignee.Scan(uuidB)

	before := dbsqlc.ProjectIssueDetail{Status: StatusTodo}
	after := dbsqlc.ProjectIssueDetail{
		Status:         StatusInProgress,
		AssigneeUserID: assignee,
		DueAt:          pgTime(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)),
	}
	if err := s.recordIssueActivity(context.Background(), fq, pgtype.UUID{}, actor, before, after); err != nil {
		t.Fatalf("recordIssueActivity: %v", err)
	}
	if len(fq.activities) != 3 {
		t.Fatalf("expected 3 activity rows (status/assignee/due), got %d", len(fq.activities))
	}
	fieldsSeen := map[string]bool{}
	for _, a := range fq.activities {
		fieldsSeen[a.Field] = true
	}
	for _, want := range []string{"status", "assignee", "due_at"} {
		if !fieldsSeen[want] {
			t.Fatalf("missing activity row for %q; got %v", want, fieldsSeen)
		}
	}
}

func TestRecordIssueActivityNoChangesNoRows(t *testing.T) {
	fq := &fakeQueries{}
	s := newTestService(fq)
	row := dbsqlc.ProjectIssueDetail{Status: StatusTodo}
	if err := s.recordIssueActivity(context.Background(), fq, pgtype.UUID{}, pgtype.UUID{}, row, row); err != nil {
		t.Fatalf("recordIssueActivity: %v", err)
	}
	if len(fq.activities) != 0 {
		t.Fatalf("no-op update wrote %d activity rows", len(fq.activities))
	}
}
