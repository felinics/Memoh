package message

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestPostgresBotDeleteAndRouteSwitchShareLockOrder(t *testing.T) {
	fixture := setupOrderedSessionFixture(t)
	botID := mustTestUUID(t, fixture.botID)
	assertDeleteAndRouteSwitchShareLockOrder(t, fixture, func(ctx context.Context, queries *dbsqlc.Queries) error {
		return queries.DeleteBotByID(ctx, botID)
	})
}

func TestPostgresBotRuntimeClearAndRouteSwitchShareLockOrder(t *testing.T) {
	fixture := setupOrderedSessionFixture(t)
	botID := mustTestUUID(t, fixture.botID)
	assertDeleteAndRouteSwitchShareLockOrder(t, fixture, func(ctx context.Context, queries *dbsqlc.Queries) error {
		return queries.ClearBotRuntimeData(ctx, botID)
	})
}

func TestPostgresBotDeleteAndRouteDeleteShareLockOrder(t *testing.T) {
	fixture := setupOrderedSessionFixture(t)
	botID := mustTestUUID(t, fixture.botID)
	assertDeleteAndRouteDeleteShareLockOrder(t, fixture, func(ctx context.Context, queries *dbsqlc.Queries) error {
		return queries.DeleteBotByID(ctx, botID)
	})
}

func TestPostgresBotRuntimeClearAndRouteDeleteShareLockOrder(t *testing.T) {
	fixture := setupOrderedSessionFixture(t)
	botID := mustTestUUID(t, fixture.botID)
	assertDeleteAndRouteDeleteShareLockOrder(t, fixture, func(ctx context.Context, queries *dbsqlc.Queries) error {
		return queries.ClearBotRuntimeData(ctx, botID)
	})
}

func TestPostgresRouteSwitchPreservesNullAndMissingDestinationSemantics(t *testing.T) {
	fixture := setupOrderedSessionFixture(t)
	ctx := context.Background()
	routeID := createActiveRoute(t, fixture)
	queries := dbsqlc.New(fixture.pool)
	if err := queries.SetRouteActiveSession(ctx, dbsqlc.SetRouteActiveSessionParams{
		ActiveSessionID: pgtype.UUID{},
		ID:              mustTestUUID(t, routeID),
	}); err != nil {
		t.Fatalf("clear active session: %v", err)
	}
	var activeSessionID pgtype.UUID
	if err := fixture.pool.QueryRow(ctx, `
		SELECT active_session_id FROM bot_channel_routes WHERE id = $1
	`, routeID).Scan(&activeSessionID); err != nil {
		t.Fatalf("read cleared active session: %v", err)
	}
	if activeSessionID.Valid {
		t.Fatalf("active session after clear = %v, want null", activeSessionID)
	}
	if err := queries.SetRouteActiveSession(ctx, dbsqlc.SetRouteActiveSessionParams{
		ActiveSessionID: mustTestUUID(t, uuid.NewString()),
		ID:              mustTestUUID(t, routeID),
	}); err == nil {
		t.Fatal("missing destination session did not fail its foreign key check")
	}
}

func assertDeleteAndRouteSwitchShareLockOrder(
	t *testing.T,
	fixture orderedSessionFixture,
	deleteBot func(context.Context, *dbsqlc.Queries) error,
) {
	t.Helper()
	ctx := context.Background()
	routeID := createActiveRoute(t, fixture)

	deleteTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin bot deletion: %v", err)
	}
	deletionOpen := true
	defer func() {
		if deletionOpen {
			_ = deleteTx.Rollback(ctx)
		}
	}()
	if _, err := deleteTx.Exec(ctx, `
		SELECT id
		FROM bot_sessions
		WHERE bot_id = $1
		ORDER BY id
		FOR UPDATE
	`, fixture.botID); err != nil {
		t.Fatalf("lock deleted sessions: %v", err)
	}

	switchTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin active session switch: %v", err)
	}
	switchOpen := true
	defer func() {
		if switchOpen {
			_ = switchTx.Rollback(ctx)
		}
	}()
	switchPID := postgresBackendPID(t, switchTx)
	switchParams := dbsqlc.SetRouteActiveSessionParams{
		ActiveSessionID: mustTestUUID(t, fixture.highSessionID),
		ID:              mustTestUUID(t, routeID),
	}
	switched := make(chan error, 1)
	go func() {
		switched <- dbsqlc.New(switchTx).SetRouteActiveSession(ctx, switchParams)
	}()
	waitForPostgresLock(t, fixture.pool, switchPID)

	deleteErr := deleteBot(ctx, dbsqlc.New(deleteTx))
	if deleteErr == nil {
		deleteErr = deleteTx.Commit(ctx)
	} else {
		_ = deleteTx.Rollback(ctx)
	}
	deletionOpen = false

	var switchErr error
	select {
	case switchErr = <-switched:
	case <-time.After(3 * time.Second):
		t.Fatal("active session switch did not resume after bot deletion")
	}
	if switchErr == nil {
		switchErr = switchTx.Commit(ctx)
	} else {
		_ = switchTx.Rollback(ctx)
	}
	switchOpen = false

	if deleteErr != nil {
		t.Fatalf("delete bot while active session switch waits: %v", deleteErr)
	}
	if switchErr != nil {
		t.Fatalf("switch active session after bot deletion: %v", switchErr)
	}
}

func assertDeleteAndRouteDeleteShareLockOrder(
	t *testing.T,
	fixture orderedSessionFixture,
	deleteBot func(context.Context, *dbsqlc.Queries) error,
) {
	t.Helper()
	ctx := context.Background()
	routeID := createActiveRoute(t, fixture)
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE bot_sessions SET route_id = $1 WHERE id = $2
	`, routeID, fixture.highSessionID); err != nil {
		t.Fatalf("link session to deleted route: %v", err)
	}

	botTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin bot deletion: %v", err)
	}
	botOpen := true
	defer func() {
		if botOpen {
			_ = botTx.Rollback(ctx)
		}
	}()
	if _, err := botTx.Exec(ctx, `
		SELECT id
		FROM bot_sessions
		WHERE bot_id = $1
		ORDER BY id
		FOR UPDATE
	`, fixture.botID); err != nil {
		t.Fatalf("lock deleted sessions: %v", err)
	}

	routeTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin route deletion: %v", err)
	}
	routeOpen := true
	defer func() {
		if routeOpen {
			_ = routeTx.Rollback(ctx)
		}
	}()
	routePID := postgresBackendPID(t, routeTx)
	routeUUID := mustTestUUID(t, routeID)
	routeDeleted := make(chan error, 1)
	go func() {
		routeDeleted <- dbsqlc.New(routeTx).DeleteChatRoute(ctx, routeUUID)
	}()
	waitForPostgresLock(t, fixture.pool, routePID)

	probeTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin route order probe: %v", err)
	}
	_, probeErr := probeTx.Exec(ctx, `
		SELECT id FROM bot_channel_routes WHERE id = $1 FOR UPDATE NOWAIT
	`, routeID)
	if rollbackErr := probeTx.Rollback(ctx); rollbackErr != nil {
		t.Fatalf("release route order probe: %v", rollbackErr)
	}
	if probeErr != nil {
		_ = botTx.Rollback(ctx)
		botOpen = false
		select {
		case <-routeDeleted:
		case <-time.After(3 * time.Second):
			t.Fatal("route deletion did not resume during failed order probe cleanup")
		}
		_ = routeTx.Rollback(ctx)
		routeOpen = false
		t.Fatalf("route deletion locked route before its sessions: %v", probeErr)
	}

	deleteErr := deleteBot(ctx, dbsqlc.New(botTx))
	if deleteErr == nil {
		deleteErr = botTx.Commit(ctx)
	} else {
		_ = botTx.Rollback(ctx)
	}
	botOpen = false

	var routeErr error
	select {
	case routeErr = <-routeDeleted:
	case <-time.After(3 * time.Second):
		t.Fatal("route deletion did not resume after bot deletion")
	}
	if routeErr == nil {
		routeErr = routeTx.Commit(ctx)
	} else {
		_ = routeTx.Rollback(ctx)
	}
	routeOpen = false

	if deleteErr != nil {
		t.Fatalf("delete bot while route deletion waits: %v", deleteErr)
	}
	if routeErr != nil {
		t.Fatalf("delete route after bot deletion: %v", routeErr)
	}
}

func createActiveRoute(t *testing.T, fixture orderedSessionFixture) string {
	t.Helper()
	ctx := context.Background()
	routeID := uuid.NewString()
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO bot_channel_routes (
			id, bot_id, channel_type, external_conversation_id, active_session_id
		)
		VALUES ($1, $2, 'local', $3, $4)
	`, routeID, fixture.botID, uuid.NewString(), fixture.lowSessionID); err != nil {
		t.Fatalf("insert active route: %v", err)
	}
	return routeID
}
