package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	runtimeworkspace "github.com/memohai/memoh/domains/runtime/workspace"
)

type persistenceQueriesStub struct{ workspaceQueries }

type transactionBeginnerStub struct {
	tx         pgx.Tx
	beginCalls int
}

func (b *transactionBeginnerStub) Begin(context.Context) (pgx.Tx, error) {
	b.beginCalls++
	return b.tx, nil
}

type snapshotTxStub struct {
	pgx.Tx
	queries       []string
	commitCalls   int
	rollbackCalls int
	commitErr     error
	scanErrAt     int
	scanErr       error
	createdAt     time.Time
	snapshotID    pgtype.UUID
	versionID     pgtype.UUID
}

func (tx *snapshotTxStub) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	tx.queries = append(tx.queries, query)
	if tx.scanErrAt == len(tx.queries) {
		return scanRowStub{scan: func(...any) error { return tx.scanErr }}
	}
	switch {
	case strings.Contains(query, "INSERT INTO runtime.snapshots"):
		return scanRowStub{scan: func(dest ...any) error {
			*(dest[0].(*pgtype.UUID)) = tx.snapshotID
			*(dest[1].(*string)) = "container-1"
			*(dest[2].(*string)) = "runtime-snapshot-1"
			*(dest[3].(*pgtype.Text)) = pgtype.Text{String: "Before change", Valid: true}
			*(dest[4].(*pgtype.Text)) = pgtype.Text{}
			*(dest[5].(*string)) = "overlayfs"
			*(dest[6].(*string)) = runtimeworkspace.SnapshotSourceManual
			*(dest[7].(*pgtype.Timestamptz)) = pgtype.Timestamptz{Time: tx.createdAt, Valid: true}
			*(dest[8].(*pgtype.UUID)) = tx.snapshotID
			return nil
		}}
	case strings.Contains(query, "COALESCE(MAX(version)"):
		return scanRowStub{scan: func(dest ...any) error {
			*(dest[0].(*int32)) = 7
			return nil
		}}
	case strings.Contains(query, "INSERT INTO runtime.container_versions"):
		return scanRowStub{scan: func(dest ...any) error {
			*(dest[0].(*pgtype.UUID)) = tx.versionID
			*(dest[1].(*string)) = "container-1"
			*(dest[2].(*pgtype.UUID)) = tx.snapshotID
			*(dest[3].(*int32)) = 7
			*(dest[4].(*pgtype.Timestamptz)) = pgtype.Timestamptz{Time: tx.createdAt, Valid: true}
			*(dest[5].(*pgtype.UUID)) = tx.snapshotID
			return nil
		}}
	default:
		return scanRowStub{scan: func(...any) error { return errors.New("unexpected query") }}
	}
}

func (tx *snapshotTxStub) Commit(context.Context) error {
	tx.commitCalls++
	return tx.commitErr
}

func (tx *snapshotTxStub) Rollback(context.Context) error {
	tx.rollbackCalls++
	return nil
}

type scanRowStub struct {
	scan func(...any) error
}

func (r scanRowStub) Scan(dest ...any) error {
	return r.scan(dest...)
}

func TestStoreRecordSnapshotVersionUsesOneTransaction(t *testing.T) {
	t.Parallel()

	tx := &snapshotTxStub{
		createdAt:  time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC),
		snapshotID: mustPostgresUUID(t, "00000000-0000-0000-0000-000000000011"),
		versionID:  mustPostgresUUID(t, "00000000-0000-0000-0000-000000000012"),
	}
	beginner := &transactionBeginnerStub{tx: tx}
	store := NewStore(persistenceQueriesStub{}, beginner)

	recorded, err := store.RecordSnapshotVersion(t.Context(), runtimeworkspace.RecordSnapshotVersionCommand{
		ContainerID:         "container-1",
		RuntimeSnapshotName: "runtime-snapshot-1",
		DisplayName:         "Before change",
		Snapshotter:         "overlayfs",
		Source:              runtimeworkspace.SnapshotSourceManual,
	})
	if err != nil {
		t.Fatalf("RecordSnapshotVersion() error = %v", err)
	}
	if beginner.beginCalls != 1 || tx.commitCalls != 1 || tx.rollbackCalls != 1 {
		t.Fatalf("transaction calls = begin:%d commit:%d rollback:%d, want 1/1/1", beginner.beginCalls, tx.commitCalls, tx.rollbackCalls)
	}
	if len(tx.queries) != 3 {
		t.Fatalf("query count = %d, want 3", len(tx.queries))
	}
	if recorded.ID != tx.versionID.String() || recorded.Version != 7 || !recorded.CreatedAt.Equal(tx.createdAt) {
		t.Fatalf("recorded = %#v", recorded)
	}
}

func TestStoreRecordSnapshotVersionRequiresTransactionBeforeStatements(t *testing.T) {
	t.Parallel()

	store := NewStore(persistenceQueriesStub{}, nil)
	_, err := store.RecordSnapshotVersion(t.Context(), runtimeworkspace.RecordSnapshotVersionCommand{
		ContainerID:         "container-1",
		RuntimeSnapshotName: "runtime-snapshot-1",
		Snapshotter:         "overlayfs",
		Source:              runtimeworkspace.SnapshotSourceManual,
	})
	if !errors.Is(err, runtimeworkspace.ErrTransactionsRequired) {
		t.Fatalf("RecordSnapshotVersion() error = %v, want ErrTransactionsRequired", err)
	}
}

func TestStoreRecordSnapshotVersionRollsBackStatementFailure(t *testing.T) {
	t.Parallel()

	statementErr := errors.New("next version failed")
	tx := &snapshotTxStub{
		scanErrAt:  2,
		scanErr:    statementErr,
		createdAt:  time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC),
		snapshotID: mustPostgresUUID(t, "00000000-0000-0000-0000-000000000011"),
		versionID:  mustPostgresUUID(t, "00000000-0000-0000-0000-000000000012"),
	}
	beginner := &transactionBeginnerStub{tx: tx}
	store := NewStore(persistenceQueriesStub{}, beginner)

	_, err := store.RecordSnapshotVersion(t.Context(), runtimeworkspace.RecordSnapshotVersionCommand{
		ContainerID:         "container-1",
		RuntimeSnapshotName: "runtime-snapshot-1",
		Snapshotter:         "overlayfs",
		Source:              runtimeworkspace.SnapshotSourceManual,
	})
	if !errors.Is(err, statementErr) {
		t.Fatalf("RecordSnapshotVersion() error = %v, want statement error", err)
	}
	if beginner.beginCalls != 1 || tx.commitCalls != 0 || tx.rollbackCalls != 1 {
		t.Fatalf("transaction calls = begin:%d commit:%d rollback:%d, want 1/0/1", beginner.beginCalls, tx.commitCalls, tx.rollbackCalls)
	}
	if len(tx.queries) != 2 {
		t.Fatalf("query count = %d, want 2", len(tx.queries))
	}
}

func TestStoreRecordSnapshotVersionReturnsCommitError(t *testing.T) {
	t.Parallel()

	commitErr := errors.New("commit failed")
	tx := &snapshotTxStub{
		commitErr:  commitErr,
		createdAt:  time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC),
		snapshotID: mustPostgresUUID(t, "00000000-0000-0000-0000-000000000011"),
		versionID:  mustPostgresUUID(t, "00000000-0000-0000-0000-000000000012"),
	}
	beginner := &transactionBeginnerStub{tx: tx}
	store := NewStore(persistenceQueriesStub{}, beginner)

	_, err := store.RecordSnapshotVersion(t.Context(), runtimeworkspace.RecordSnapshotVersionCommand{
		ContainerID:         "container-1",
		RuntimeSnapshotName: "runtime-snapshot-1",
		Snapshotter:         "overlayfs",
		Source:              runtimeworkspace.SnapshotSourceManual,
	})
	if !errors.Is(err, commitErr) {
		t.Fatalf("RecordSnapshotVersion() error = %v, want commit error", err)
	}
	if beginner.beginCalls != 1 || tx.commitCalls != 1 || tx.rollbackCalls != 1 {
		t.Fatalf("transaction calls = begin:%d commit:%d rollback:%d, want 1/1/1", beginner.beginCalls, tx.commitCalls, tx.rollbackCalls)
	}
	if len(tx.queries) != 3 {
		t.Fatalf("query count = %d, want 3", len(tx.queries))
	}
}

func mustPostgresUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	parsed, err := uuid.Parse(value)
	if err != nil {
		t.Fatalf("parse UUID %q: %v", value, err)
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}
}
