package workspace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	runtimesqlc "github.com/memohai/memoh/domains/runtime/internal/postgres/sqlc"
	runtimeworkspace "github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/db"
)

const (
	testBotID          = "11111111-1111-4111-8111-111111111111"
	testRuntimeID      = "22222222-2222-4222-8222-222222222222"
	testTargetID       = "33333333-3333-4333-8333-333333333333"
	testRuntimeOwnerID = "44444444-4444-4444-8444-444444444444"
)

type mountQueriesStub struct {
	clear  func(context.Context, pgtype.UUID) error
	create func(context.Context, runtimesqlc.CreateOrUpdateBotRemoteRuntimeMountParams) (pgtype.UUID, error)
	delete func(context.Context, runtimesqlc.DeleteBotRemoteRuntimeMountParams) (pgtype.UUID, error)
	get    func(context.Context, runtimesqlc.GetBotRemoteRuntimeMountParams) (runtimesqlc.GetBotRemoteRuntimeMountRow, error)
	getPri func(context.Context, pgtype.UUID) (runtimesqlc.GetPrimaryBotRemoteRuntimeMountRow, error)
	list   func(context.Context, pgtype.UUID) ([]runtimesqlc.ListBotRemoteRuntimeMountsRow, error)
	set    func(context.Context, runtimesqlc.SetBotRemoteRuntimePrimaryParams) (int64, error)
	update func(context.Context, runtimesqlc.UpdateBotRemoteRuntimeMountToolApprovalParams) (pgtype.UUID, error)
}

func (s mountQueriesStub) ClearBotRemoteRuntimePrimary(ctx context.Context, id pgtype.UUID) error {
	return s.clear(ctx, id)
}

func (s mountQueriesStub) CreateOrUpdateBotRemoteRuntimeMount(ctx context.Context, arg runtimesqlc.CreateOrUpdateBotRemoteRuntimeMountParams) (pgtype.UUID, error) {
	return s.create(ctx, arg)
}

func (s mountQueriesStub) DeleteBotRemoteRuntimeMount(ctx context.Context, arg runtimesqlc.DeleteBotRemoteRuntimeMountParams) (pgtype.UUID, error) {
	return s.delete(ctx, arg)
}

func (s mountQueriesStub) GetBotRemoteRuntimeMount(ctx context.Context, arg runtimesqlc.GetBotRemoteRuntimeMountParams) (runtimesqlc.GetBotRemoteRuntimeMountRow, error) {
	return s.get(ctx, arg)
}

func (s mountQueriesStub) GetPrimaryBotRemoteRuntimeMount(ctx context.Context, id pgtype.UUID) (runtimesqlc.GetPrimaryBotRemoteRuntimeMountRow, error) {
	return s.getPri(ctx, id)
}

func (s mountQueriesStub) ListBotRemoteRuntimeMounts(ctx context.Context, id pgtype.UUID) ([]runtimesqlc.ListBotRemoteRuntimeMountsRow, error) {
	return s.list(ctx, id)
}

func (s mountQueriesStub) SetBotRemoteRuntimePrimary(ctx context.Context, arg runtimesqlc.SetBotRemoteRuntimePrimaryParams) (int64, error) {
	return s.set(ctx, arg)
}

func (s mountQueriesStub) UpdateBotRemoteRuntimeMountToolApproval(ctx context.Context, arg runtimesqlc.UpdateBotRemoteRuntimeMountToolApprovalParams) (pgtype.UUID, error) {
	return s.update(ctx, arg)
}

type transactionBeginnerFake struct {
	tx    pgx.Tx
	err   error
	calls int
}

func (b *transactionBeginnerFake) Begin(context.Context) (pgx.Tx, error) {
	b.calls++
	return b.tx, b.err
}

type transactionFake struct {
	pgx.Tx
	commits, rollbacks int
	commitErr          error
}

func (tx *transactionFake) Commit(context.Context) error   { tx.commits++; return tx.commitErr }
func (tx *transactionFake) Rollback(context.Context) error { tx.rollbacks++; return nil }

func TestRemoteMountStoreGetMapsRecordAndCopiesApproval(t *testing.T) {
	raw := []byte(`{"enabled":true}`)
	queries := mountQueriesStub{
		get: func(_ context.Context, arg runtimesqlc.GetBotRemoteRuntimeMountParams) (runtimesqlc.GetBotRemoteRuntimeMountRow, error) {
			if arg.BotID.String() != testBotID || arg.TargetID.String() != testTargetID {
				t.Fatalf("GetBotRemoteRuntimeMount params = %#v", arg)
			}
			return mountSQLCRecord(t, raw), nil
		},
	}
	store := newRemoteMountStore(queries, nil, nil)
	record, err := store.GetMount(context.Background(), testBotID, testTargetID)
	if err != nil {
		t.Fatalf("GetMount() error = %v", err)
	}
	if record.ID != testTargetID || record.BotID != testBotID || record.RuntimeID != testRuntimeID || record.RuntimeName != "Workstation" || record.RuntimeUserID != testRuntimeOwnerID || record.BotOwnerUserID != "" || !record.IsPrimary || record.RuntimeRevoked {
		t.Fatalf("GetMount() = %#v", record)
	}
	raw[0] = 'x'
	if string(record.ToolApproval) != `{"enabled":true}` {
		t.Fatalf("ToolApproval aliases SQLC bytes: %q", record.ToolApproval)
	}
}

func TestRemoteMountStoreCreateValidatesRuntimeOwner(t *testing.T) {
	queries := mountQueriesStub{
		create: func(_ context.Context, arg runtimesqlc.CreateOrUpdateBotRemoteRuntimeMountParams) (pgtype.UUID, error) {
			if arg.BotID.String() != testBotID || arg.RuntimeID.String() != testRuntimeID || arg.OwnerUserID.String() != testRuntimeOwnerID {
				t.Fatalf("CreateOrUpdateBotRemoteRuntimeMount params = %#v", arg)
			}
			return mustUUID(t, testTargetID), nil
		},
		get: func(context.Context, runtimesqlc.GetBotRemoteRuntimeMountParams) (runtimesqlc.GetBotRemoteRuntimeMountRow, error) {
			return mountSQLCRecord(t, nil), nil
		},
	}
	store := newRemoteMountStore(queries, nil, nil)
	if _, err := store.CreateOrUpdateMount(t.Context(), testBotID, testRuntimeID, testRuntimeOwnerID); err != nil {
		t.Fatalf("CreateOrUpdateMount() error = %v", err)
	}
}

func TestRemoteMountStoreMapsRevokedRuntime(t *testing.T) {
	row := mountSQLCRecord(t, nil)
	row.RevokedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	store := newRemoteMountStore(mountQueriesStub{
		get: func(context.Context, runtimesqlc.GetBotRemoteRuntimeMountParams) (runtimesqlc.GetBotRemoteRuntimeMountRow, error) {
			return row, nil
		},
	}, nil, nil)

	record, err := store.GetMount(t.Context(), testBotID, testTargetID)
	if err != nil {
		t.Fatalf("GetMount() error = %v", err)
	}
	if !record.RuntimeRevoked {
		t.Fatalf("GetMount() = %#v, want revoked runtime", record)
	}
}

func TestRemoteMountStoreSetPrimaryUsesTransactionScopedQueries(t *testing.T) {
	var operations []string
	txQueries := mountQueriesStub{
		clear: func(_ context.Context, id pgtype.UUID) error {
			operations = append(operations, "clear")
			if id.String() != testBotID {
				t.Fatalf("clear bot = %s", id.String())
			}
			return nil
		},
		set: func(_ context.Context, arg runtimesqlc.SetBotRemoteRuntimePrimaryParams) (int64, error) {
			operations = append(operations, "set")
			if arg.BotID.String() != testBotID || arg.TargetID.String() != testTargetID {
				t.Fatalf("set params = %#v", arg)
			}
			return 1, nil
		},
	}
	pgxTx := &transactionFake{}
	beginner := &transactionBeginnerFake{tx: pgxTx}
	store := newRemoteMountStore(mountQueriesStub{}, beginner, func(got pgx.Tx) mountQueries {
		if got != pgxTx {
			t.Fatalf("bound transaction = %T, want test transaction", got)
		}
		return txQueries
	})
	if err := store.SetPrimary(context.Background(), testBotID, testTargetID); err != nil {
		t.Fatalf("SetPrimary() error = %v", err)
	}
	if beginner.calls != 1 || pgxTx.commits != 1 || pgxTx.rollbacks != 1 || len(operations) != 2 || operations[0] != "clear" || operations[1] != "set" {
		t.Fatalf("transaction calls = %d/%d/%d, operations = %v", beginner.calls, pgxTx.commits, pgxTx.rollbacks, operations)
	}
}

func TestRemoteMountStoreSetPrimaryRequiresTransactionAndMapsMissingTarget(t *testing.T) {
	store := newRemoteMountStore(mountQueriesStub{}, nil, nil)
	if err := store.SetPrimary(context.Background(), testBotID, testTargetID); !errors.Is(err, runtimeworkspace.ErrTransactionsRequired) {
		t.Fatalf("SetPrimary() error = %v, want transactions unsupported", err)
	}

	txQueries := mountQueriesStub{
		clear: func(context.Context, pgtype.UUID) error { return nil },
		set:   func(context.Context, runtimesqlc.SetBotRemoteRuntimePrimaryParams) (int64, error) { return 0, nil },
	}
	store = newRemoteMountStore(mountQueriesStub{}, &transactionBeginnerFake{tx: &transactionFake{}}, func(pgx.Tx) mountQueries { return txQueries })
	if err := store.SetPrimary(context.Background(), testBotID, testTargetID); !errors.Is(err, runtimeworkspace.ErrRecordNotFound) {
		t.Fatalf("SetPrimary() error = %v, want runtimeworkspace.ErrRecordNotFound", err)
	}
}

func TestRemoteMountStoreSetPrimaryRollsBackCallbackError(t *testing.T) {
	wantErr := errors.New("clear failed")
	pgxTx := &transactionFake{}
	queries := mountQueriesStub{clear: func(context.Context, pgtype.UUID) error { return wantErr }}
	store := newRemoteMountStore(mountQueriesStub{}, &transactionBeginnerFake{tx: pgxTx}, func(pgx.Tx) mountQueries { return queries })
	err := store.SetPrimary(t.Context(), testBotID, testTargetID)
	if !errors.Is(err, wantErr) || pgxTx.commits != 0 || pgxTx.rollbacks != 1 {
		t.Fatalf("SetPrimary() = %v, commit/rollback = %d/%d", err, pgxTx.commits, pgxTx.rollbacks)
	}
}

func TestRemoteMountStoreMapsNoRows(t *testing.T) {
	queries := mountQueriesStub{
		get: func(context.Context, runtimesqlc.GetBotRemoteRuntimeMountParams) (runtimesqlc.GetBotRemoteRuntimeMountRow, error) {
			return runtimesqlc.GetBotRemoteRuntimeMountRow{}, pgx.ErrNoRows
		},
	}
	store := newRemoteMountStore(queries, nil, nil)
	if _, err := store.GetMount(context.Background(), testBotID, testTargetID); !errors.Is(err, runtimeworkspace.ErrRecordNotFound) {
		t.Fatalf("GetMount() error = %v, want runtimeworkspace.ErrRecordNotFound", err)
	}
}

func mountSQLCRecord(t *testing.T, approval []byte) runtimesqlc.GetBotRemoteRuntimeMountRow {
	t.Helper()
	id := mustUUID(t, testTargetID)
	botID := mustUUID(t, testBotID)
	runtimeID := mustUUID(t, testRuntimeID)
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	return runtimesqlc.GetBotRemoteRuntimeMountRow{
		ID: id, BotID: botID, RuntimeID: runtimeID, IsPrimary: true,
		ToolApprovalConfig: approval, RuntimeName: "Workstation", RuntimeUserID: mustUUID(t, testRuntimeOwnerID),
		CreatedAt: pgtype.Timestamptz{Time: now, Valid: true}, UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}
}

func mustUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := db.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

var _ runtimeworkspace.RemoteMountStore = (*RemoteMountStore)(nil)
