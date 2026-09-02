package workspacedeps

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const (
	testBotID = "20000000-0000-4000-8000-000000000001"
	testRowID = "30000000-0000-4000-8000-000000000001"
)

// fakeDependencyQueries overrides only the dependency installation methods;
// every other dbstore.Queries method panics on the nil embedded interface,
// which is fine because the store never calls them.
type fakeDependencyQueries struct {
	dbstore.Queries
	get      func(dbsqlc.GetBotDependencyInstallationParams) (dbsqlc.BotDependencyInstallation, error)
	del      func(dbsqlc.DeleteBotDependencyInstallationParams) (int64, error)
	stale    func(float64) ([]dbsqlc.BotDependencyInstallation, error)
	status   func(dbsqlc.UpdateBotDependencyInstallationStatusParams) (dbsqlc.BotDependencyInstallation, error)
	observed func(dbsqlc.UpdateBotDependencyInstallationObservedParams) (dbsqlc.BotDependencyInstallation, error)
}

func (f *fakeDependencyQueries) GetBotDependencyInstallation(_ context.Context, arg dbsqlc.GetBotDependencyInstallationParams) (dbsqlc.BotDependencyInstallation, error) {
	return f.get(arg)
}

func (f *fakeDependencyQueries) DeleteBotDependencyInstallation(_ context.Context, arg dbsqlc.DeleteBotDependencyInstallationParams) (int64, error) {
	return f.del(arg)
}

func (f *fakeDependencyQueries) ListStaleBotDependencyOperations(_ context.Context, olderThanSeconds float64) ([]dbsqlc.BotDependencyInstallation, error) {
	return f.stale(olderThanSeconds)
}

func (f *fakeDependencyQueries) UpdateBotDependencyInstallationStatus(_ context.Context, arg dbsqlc.UpdateBotDependencyInstallationStatusParams) (dbsqlc.BotDependencyInstallation, error) {
	return f.status(arg)
}

func (f *fakeDependencyQueries) UpdateBotDependencyInstallationObserved(_ context.Context, arg dbsqlc.UpdateBotDependencyInstallationObservedParams) (dbsqlc.BotDependencyInstallation, error) {
	return f.observed(arg)
}

func mustUUID(t *testing.T, id string) pgtype.UUID {
	t.Helper()
	parsed, err := db.ParseUUID(id)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", id, err)
	}
	return parsed
}

func testKey() InstallationKey {
	return InstallationKey{BotID: testBotID, WorkspaceTargetID: "native", DependencyID: "codex"}
}

func TestInstallationFromRow(t *testing.T) {
	created := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	updated := created.Add(time.Minute)
	checked := created.Add(2 * time.Minute)
	row := dbsqlc.BotDependencyInstallation{
		ID:                mustUUID(t, testRowID),
		BotID:             mustUUID(t, testBotID),
		WorkspaceTargetID: "native",
		DependencyID:      "codex",
		Source:            InstallationSourceManaged,
		Status:            string(StatusInstalled),
		InstalledVersion:  "0.151.0",
		LatestVersion:     "0.152.0",
		LastCheckedAt:     pgtype.Timestamptz{Time: checked, Valid: true},
		LastError:         "boom",
		ManifestDigest:    "sha256:abc",
		CreatedAt:         pgtype.Timestamptz{Time: created, Valid: true},
		UpdatedAt:         pgtype.Timestamptz{Time: updated, Valid: true},
	}

	got := installationFromRow(row)

	if got.ID != testRowID || got.BotID != testBotID {
		t.Fatalf("ids = %q/%q, want %q/%q", got.ID, got.BotID, testRowID, testBotID)
	}
	if got.WorkspaceTargetID != "native" || got.DependencyID != "codex" {
		t.Fatalf("key = %q/%q", got.WorkspaceTargetID, got.DependencyID)
	}
	if got.Source != InstallationSourceManaged || got.Status != StatusInstalled {
		t.Fatalf("source/status = %q/%q", got.Source, got.Status)
	}
	if got.InstalledVersion != "0.151.0" || got.LatestVersion != "0.152.0" || got.LastError != "boom" || got.ManifestDigest != "sha256:abc" {
		t.Fatalf("scalar columns mismatch: %+v", got)
	}
	if got.LastCheckedAt == nil || !got.LastCheckedAt.Equal(checked) {
		t.Fatalf("LastCheckedAt = %v, want %v", got.LastCheckedAt, checked)
	}
	if !got.CreatedAt.Equal(created) || !got.UpdatedAt.Equal(updated) {
		t.Fatalf("timestamps = %v/%v", got.CreatedAt, got.UpdatedAt)
	}

	row.LastCheckedAt = pgtype.Timestamptz{}
	if installationFromRow(row).LastCheckedAt != nil {
		t.Fatal("NULL last_checked_at must map to a nil pointer")
	}
}

func TestObservedParamsNilLeavesColumnsUntouched(t *testing.T) {
	botID := mustUUID(t, testBotID)
	params := observedParams(botID, testKey(), ObservedUpdate{})

	if params.Source.Valid || params.InstalledVersion.Valid || params.LatestVersion.Valid ||
		params.LastCheckedAt.Valid || params.LastError.Valid || params.ManifestDigest.Valid {
		t.Fatalf("nil fields must become NULL parameters: %+v", params)
	}
	if params.BotID != botID || params.WorkspaceTargetID != "native" || params.DependencyID != "codex" {
		t.Fatalf("key not carried: %+v", params)
	}
}

func TestObservedParamsWritesProvidedValues(t *testing.T) {
	checked := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	source := InstallationSourceImage
	empty := ""
	version := "2.1.250"
	params := observedParams(mustUUID(t, testBotID), testKey(), ObservedUpdate{
		Source:        &source,
		LatestVersion: &version,
		LastCheckedAt: &checked,
		LastError:     &empty,
	})

	if !params.Source.Valid || params.Source.String != InstallationSourceImage {
		t.Fatalf("source = %+v", params.Source)
	}
	if !params.LatestVersion.Valid || params.LatestVersion.String != version {
		t.Fatalf("latest_version = %+v", params.LatestVersion)
	}
	if !params.LastCheckedAt.Valid || !params.LastCheckedAt.Time.Equal(checked) {
		t.Fatalf("last_checked_at = %+v", params.LastCheckedAt)
	}
	// A pointer to "" clears the column; only nil means "leave it".
	if !params.LastError.Valid || params.LastError.String != "" {
		t.Fatalf("last_error = %+v, want a valid empty string", params.LastError)
	}
	if params.InstalledVersion.Valid || params.ManifestDigest.Valid {
		t.Fatalf("untouched fields must stay NULL: %+v", params)
	}
}

func TestPostgresStoreMapsNoRowsToNotFound(t *testing.T) {
	q := &fakeDependencyQueries{
		get: func(dbsqlc.GetBotDependencyInstallationParams) (dbsqlc.BotDependencyInstallation, error) {
			return dbsqlc.BotDependencyInstallation{}, pgx.ErrNoRows
		},
		status: func(dbsqlc.UpdateBotDependencyInstallationStatusParams) (dbsqlc.BotDependencyInstallation, error) {
			return dbsqlc.BotDependencyInstallation{}, pgx.ErrNoRows
		},
		observed: func(dbsqlc.UpdateBotDependencyInstallationObservedParams) (dbsqlc.BotDependencyInstallation, error) {
			return dbsqlc.BotDependencyInstallation{}, pgx.ErrNoRows
		},
	}
	store := NewPostgresStore(q)
	ctx := context.Background()

	if _, err := store.Get(ctx, testKey()); !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("Get error = %v, want ErrInstallationNotFound", err)
	}
	if _, err := store.SetStatus(ctx, testKey(), StatusFailed, "x"); !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("SetStatus error = %v, want ErrInstallationNotFound", err)
	}
	if _, err := store.UpdateObserved(ctx, testKey(), ObservedUpdate{}); !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("UpdateObserved error = %v, want ErrInstallationNotFound", err)
	}
}

func TestPostgresStoreWrapsOtherErrors(t *testing.T) {
	boom := errors.New("connection reset")
	q := &fakeDependencyQueries{
		get: func(dbsqlc.GetBotDependencyInstallationParams) (dbsqlc.BotDependencyInstallation, error) {
			return dbsqlc.BotDependencyInstallation{}, boom
		},
	}
	_, err := NewPostgresStore(q).Get(context.Background(), testKey())
	if !errors.Is(err, boom) {
		t.Fatalf("Get error = %v, want wrapped %v", err, boom)
	}
	if errors.Is(err, ErrInstallationNotFound) {
		t.Fatal("a transport error must not read as not found")
	}
}

func TestPostgresStoreDeleteReportsNotFound(t *testing.T) {
	var affected int64
	q := &fakeDependencyQueries{
		del: func(arg dbsqlc.DeleteBotDependencyInstallationParams) (int64, error) {
			if arg.WorkspaceTargetID != "native" || arg.DependencyID != "codex" {
				t.Fatalf("unexpected key: %+v", arg)
			}
			return affected, nil
		},
	}
	store := NewPostgresStore(q)

	if err := store.Delete(context.Background(), testKey()); !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("Delete of missing row error = %v, want ErrInstallationNotFound", err)
	}
	affected = 1
	if err := store.Delete(context.Background(), testKey()); err != nil {
		t.Fatalf("Delete error = %v", err)
	}
}

func TestPostgresStoreRejectsInvalidBotID(t *testing.T) {
	store := NewPostgresStore(&fakeDependencyQueries{})
	key := InstallationKey{BotID: "not-a-uuid", WorkspaceTargetID: "native", DependencyID: "codex"}

	if _, err := store.Get(context.Background(), key); err == nil || errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("Get error = %v, want a parse error", err)
	}
	if err := store.Delete(context.Background(), key); err == nil {
		t.Fatal("Delete with an invalid bot id must fail")
	}
}

func TestPostgresStoreStaleSecondsArgument(t *testing.T) {
	var seen []float64
	q := &fakeDependencyQueries{
		stale: func(olderThanSeconds float64) ([]dbsqlc.BotDependencyInstallation, error) {
			seen = append(seen, olderThanSeconds)
			return nil, nil
		},
	}
	store := NewPostgresStore(q)
	for _, d := range []time.Duration{90 * time.Second, 0, -time.Minute, 1500 * time.Millisecond} {
		rows, err := store.ListStaleOperations(context.Background(), d)
		if err != nil {
			t.Fatalf("ListStaleOperations(%v): %v", d, err)
		}
		if rows == nil || len(rows) != 0 {
			t.Fatalf("ListStaleOperations(%v) = %v, want an empty non-nil slice", d, rows)
		}
	}
	want := []float64{90, 0, 0, 1.5}
	if len(seen) != len(want) {
		t.Fatalf("seen = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("seen[%d] = %v, want %v", i, seen[i], want[i])
		}
	}
}
