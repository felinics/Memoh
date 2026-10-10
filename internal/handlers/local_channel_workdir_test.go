package handlers

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/workdir"
)

// ownedWorkdirStore answers GetWorkdir the way the SQL query does: a row is
// visible only under the bot that owns it, and archived rows are returned so
// the workdir service's own archive check is what refuses them.
type ownedWorkdirStore struct {
	dbstore.BotWorkdirStore
	records []dbstore.BotWorkdirRecord
}

func (s ownedWorkdirStore) GetWorkdir(_ context.Context, botID, workdirID string) (dbstore.BotWorkdirRecord, error) {
	for _, record := range s.records {
		if record.ID == workdirID && record.BotID == botID {
			return record, nil
		}
	}
	return dbstore.BotWorkdirRecord{}, db.ErrNotFound
}

// TestLocalChannelWSFirstSendBindsRequestedWorkdir pins the contract the Web
// first send relies on when it creates its session in-band: the requested
// workdir is validated by the same rule as the REST create, a valid one is
// bound and echoed on session_created, and an invalid one fails the send
// before any session row is written.
func TestLocalChannelWSFirstSendBindsRequestedWorkdir(t *testing.T) {
	t.Parallel()

	const (
		botID            = "11111111-1111-1111-1111-111111111111"
		otherBotID       = "99999999-9999-9999-9999-999999999999"
		sessionID        = "22222222-2222-2222-2222-222222222222"
		currentUser      = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		activeWorkdirID  = "33333333-3333-3333-3333-333333333333"
		archivedWorkdir  = "44444444-4444-4444-4444-444444444444"
		foreignWorkdirID = "55555555-5555-5555-5555-555555555555"
	)
	workdirs := workdir.NewService(ownedWorkdirStore{records: []dbstore.BotWorkdirRecord{
		{ID: activeWorkdirID, BotID: botID, TargetKind: workdir.TargetKindNative, Path: "/data/project"},
		{ID: archivedWorkdir, BotID: botID, TargetKind: workdir.TargetKindNative, Path: "/data/old", ArchivedAt: time.Now()},
		{ID: foreignWorkdirID, BotID: otherBotID, TargetKind: workdir.TargetKindNative, Path: "/data/theirs"},
	}}, nil)

	tests := []struct {
		name          string
		workdirID     string
		wantCreated   bool
		wantWorkdirID string
		wantCode      string
	}{
		{name: "active workdir of this bot", workdirID: activeWorkdirID, wantCreated: true, wantWorkdirID: activeWorkdirID},
		{name: "no workdir", wantCreated: true},
		{name: "archived workdir", workdirID: archivedWorkdir, wantCode: "http.conflict"},
		{name: "workdir of another bot", workdirID: foreignWorkdirID, wantCode: "workdir.not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var (
				mu      sync.Mutex
				created []sqlc.CreateSessionParams
				row     sqlc.BotSession
			)
			queries := localChannelSessionAuthQueries{
				bot: testBotRow(botID, map[string]any{}),
				grants: []sqlc.ListBotUserGrantsForUserRow{{
					ID:          testUUID("dddddddd-dddd-dddd-dddd-dddddddddddd"),
					BotID:       testUUID(botID),
					SubjectType: bots.GrantSubjectUser,
					UserID:      testUUID(currentUser),
					Permissions: []byte(`["chat","workspace_exec"]`),
				}},
				createSession: func(_ context.Context, params sqlc.CreateSessionParams) (sqlc.BotSession, error) {
					mu.Lock()
					defer mu.Unlock()
					created = append(created, params)
					now := time.Now()
					row = sqlc.BotSession{
						ID:              testUUID(sessionID),
						BotID:           params.BotID,
						ChannelType:     params.ChannelType,
						Type:            params.Type,
						SessionMode:     params.SessionMode,
						RuntimeType:     params.RuntimeType,
						RuntimeMetadata: params.RuntimeMetadata,
						Metadata:        params.Metadata,
						CreatedByUserID: params.CreatedByUserID,
						WorkdirID:       params.WorkdirID,
						CreatedAt:       pgtype.Timestamptz{Time: now, Valid: true},
						UpdatedAt:       pgtype.Timestamptz{Time: now, Valid: true},
					}
					return row, nil
				},
				getSessionByID: func(context.Context, pgtype.UUID) (sqlc.BotSession, error) {
					mu.Lock()
					defer mu.Unlock()
					if len(created) == 0 {
						return sqlc.BotSession{}, db.ErrNotFound
					}
					return row, nil
				},
			}
			handler := &LocalChannelHandler{
				channelType:    channel.ChannelTypeLocal,
				botService:     bots.NewService(nil, queries),
				accountService: accounts.NewService(nil, testAdminAccountStore{role: "user"}),
				sessionService: sessionpkg.NewService(nil, queries, nil),
				agentService:   &application.Service{},
				workdirs:       workdirs,
				wsSkillTurns:   newWSRequestedSkillTurnRegistry(),
				logger:         slog.Default(),
			}
			client := openLocalChannelTestWS(t, handler, botID, currentUser)

			message := map[string]any{
				"type":          "message",
				"invocation_id": "invocation-1",
				"text":          "hello",
			}
			if tt.workdirID != "" {
				message["workdir_id"] = tt.workdirID
			}
			if err := client.WriteJSON(message); err != nil {
				t.Fatalf("write ws message: %v", err)
			}
			var event map[string]any
			if err := client.ReadJSON(&event); err != nil {
				t.Fatalf("read ws event: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if !tt.wantCreated {
				if got := event["type"]; got != "error" {
					t.Fatalf("event type = %#v, want error; event=%#v", got, event)
				}
				if got := event["code"]; got != tt.wantCode {
					t.Fatalf("error code = %#v, want %q", got, tt.wantCode)
				}
				if got := event["invocation_id"]; got != "invocation-1" {
					t.Fatalf("error invocation_id = %#v, want invocation-1", got)
				}
				if len(created) != 0 {
					t.Fatalf("CreateSession called %d times, want 0", len(created))
				}
				return
			}

			if got := event["type"]; got != "session_created" {
				t.Fatalf("event type = %#v, want session_created; event=%#v", got, event)
			}
			if got := event["session_id"]; got != sessionID {
				t.Fatalf("session_created session_id = %#v, want %q", got, sessionID)
			}
			gotWorkdir, hasWorkdir := event["workdir_id"]
			if tt.wantWorkdirID == "" {
				if hasWorkdir {
					t.Fatalf("session_created carries workdir_id %#v for an unbound session", gotWorkdir)
				}
			} else if gotWorkdir != tt.wantWorkdirID {
				t.Fatalf("session_created workdir_id = %#v, want %q", gotWorkdir, tt.wantWorkdirID)
			}
			if len(created) != 1 {
				t.Fatalf("CreateSession called %d times, want 1", len(created))
			}
			params := created[0]
			if tt.wantWorkdirID == "" {
				if params.WorkdirID.Valid {
					t.Fatalf("created session workdir = %s, want none", params.WorkdirID.String())
				}
			} else if got := params.WorkdirID.String(); got != tt.wantWorkdirID {
				t.Fatalf("created session workdir = %q, want %q", got, tt.wantWorkdirID)
			}
			if got := params.RuntimeType; got != sessionpkg.RuntimeModel {
				t.Fatalf("created session runtime = %q, want %q", got, sessionpkg.RuntimeModel)
			}
		})
	}
}
