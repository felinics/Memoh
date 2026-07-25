package postgres

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"

	channelbackup "github.com/memohai/memoh/domains/channel/backup"
	dbpkg "github.com/memohai/memoh/internal/db"
)

func TestPostgresImportRetriesAndCompensatesObservations(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("skip postgres integration test: TEST_POSTGRES_DSN is not set")
	}
	ctx := t.Context()
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Skipf("skip postgres integration test: cannot connect to database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("skip postgres integration test: database ping failed: %v", err)
	}

	userID := uuid.NewString()
	botID := uuid.NewString()
	routeID := uuid.NewString()
	sessionID := uuid.NewString()
	name := "channel-backup-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO iam.users (id, username, is_active)
			VALUES ($1, $2, true)
			RETURNING id
		)
		INSERT INTO iam.team_members (user_id, role)
		SELECT id, 'admin' FROM created_user
	`, userID, name); err != nil {
		t.Fatalf("create fixture user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO api.bots (id, owner_user_id, name) VALUES ($1, $2, $3)",
		botID, userID, name,
	); err != nil {
		t.Fatalf("create fixture bot: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO channel.bot_channel_routes (
			id, bot_id, channel_type, external_conversation_id
		) VALUES ($1, $2, 'local', $3)
	`, routeID, botID, uuid.NewString()); err != nil {
		t.Fatalf("create fixture route: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent.bot_sessions (
			id, bot_id, route_id, channel_type, type, session_mode, runtime_type
		) VALUES ($1, $2, $3, 'local', 'chat', 'chat', 'model')
	`, sessionID, botID, routeID); err != nil {
		t.Fatalf("create fixture session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM channel.bot_session_events WHERE bot_id = $1", botID)
		_, _ = pool.Exec(ctx, "DELETE FROM channel.bot_session_discuss_cursors WHERE bot_id = $1", botID)
		_, _ = pool.Exec(ctx, "DELETE FROM agent.bot_sessions WHERE bot_id = $1", botID)
		_, _ = pool.Exec(ctx, "DELETE FROM channel.bot_channel_routes WHERE bot_id = $1", botID)
		_, _ = pool.Exec(ctx, "DELETE FROM api.bots WHERE id = $1", botID)
		_, _ = pool.Exec(ctx, "DELETE FROM iam.users WHERE id = $1", userID)
	})

	store, err := New(pool, exclusiveBotLock{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	oldRouteID := uuid.NewString()
	oldSessionID := uuid.NewString()
	oldEventID := uuid.NewString()
	externalID := "external-" + uuid.NewString()
	request := channelbackup.ImportRequest{
		BotID: botID,
		DiscussCursors: []channelbackup.DiscussCursor{{
			SessionID: oldSessionID, ScopeKey: "default", RouteID: &oldRouteID,
			Source: "discuss", ConsumedCursor: 42,
		}},
		SessionEvents: []channelbackup.SessionEvent{{
			ID: oldEventID, BotID: uuid.NewString(), SessionID: oldSessionID,
			EventKind: "message", EventData: json.RawMessage(`{"text":"hello"}`),
			ExternalMessageID: &externalID, ReceivedAtMS: 123,
		}},
		RouteSessions: []channelbackup.RouteActiveSession{{
			RouteID: oldRouteID, SessionID: oldSessionID,
		}},
		SessionIDs: map[string]string{oldSessionID: sessionID},
		RouteIDs:   map[string]string{oldRouteID: routeID},
	}

	first, err := store.Import(ctx, request)
	if err != nil {
		t.Fatalf("first Import() error = %v", err)
	}
	second, err := store.Import(ctx, request)
	if err != nil {
		t.Fatalf("retry Import() error = %v", err)
	}
	if second.EventIDs[oldEventID] != first.EventIDs[oldEventID] {
		t.Fatalf("retry event id = %s, want %s", second.EventIDs[oldEventID], first.EventIDs[oldEventID])
	}

	var (
		eventCount int
		cursor     int64
		activeID   string
	)
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM channel.bot_session_events
		WHERE bot_id = $1 AND session_id = $2 AND external_message_id = $3
	`, botID, sessionID, externalID).Scan(&eventCount); err != nil {
		t.Fatalf("count imported events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("imported event count = %d, want 1", eventCount)
	}
	if err := pool.QueryRow(ctx, `
		SELECT consumed_cursor FROM channel.bot_session_discuss_cursors
		WHERE session_id = $1 AND scope_key = 'default'
	`, sessionID).Scan(&cursor); err != nil {
		t.Fatalf("read imported cursor: %v", err)
	}
	if cursor != 42 {
		t.Fatalf("cursor = %d, want 42", cursor)
	}
	if err := pool.QueryRow(ctx,
		"SELECT active_session_id::text FROM channel.bot_channel_routes WHERE id = $1",
		routeID,
	).Scan(&activeID); err != nil {
		t.Fatalf("read route active session: %v", err)
	}
	if activeID != sessionID {
		t.Fatalf("active session = %s, want %s", activeID, sessionID)
	}

	snapshot, err := store.Export(ctx, botID)
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if len(snapshot.DiscussCursors) != 1 ||
		snapshot.DiscussCursors[0].SessionID != sessionID ||
		snapshot.DiscussCursors[0].ScopeKey != "default" ||
		snapshot.DiscussCursors[0].ConsumedCursor != 42 {
		t.Fatalf("exported discuss cursors = %+v", snapshot.DiscussCursors)
	}
	if len(snapshot.SessionEvents) != 1 ||
		snapshot.SessionEvents[0].ID != second.EventIDs[oldEventID] {
		t.Fatalf("exported session events = %+v", snapshot.SessionEvents)
	}
	if len(snapshot.RouteActiveSessions) != 1 ||
		snapshot.RouteActiveSessions[0].RouteID != routeID ||
		snapshot.RouteActiveSessions[0].SessionID != sessionID {
		t.Fatalf("exported route active sessions = %+v", snapshot.RouteActiveSessions)
	}

	if err := store.Compensate(ctx, second.Receipt); err != nil {
		t.Fatalf("Compensate() error = %v", err)
	}
	if err := store.Compensate(ctx, second.Receipt); err != nil {
		t.Fatalf("retry Compensate() error = %v", err)
	}
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM channel.bot_session_events WHERE id = $1",
		second.EventIDs[oldEventID],
	).Scan(&eventCount); err != nil {
		t.Fatalf("count compensated events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("compensated event count = %d, want 0", eventCount)
	}
}
