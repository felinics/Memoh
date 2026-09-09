package thread

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type testACPSetupValidator struct{}

func (testACPSetupValidator) ValidateACPSetup(agentID string, metadata map[string]any) ACPSetupValidation {
	agentID = strings.ToLower(strings.TrimSpace(agentID))
	if agentID != "custom-agent" {
		return ACPSetupValidation{}
	}

	result := ACPSetupValidation{Known: true}
	acp, _ := metadata["acp"].(map[string]any)
	agents, _ := acp["agents"].(map[string]any)
	config, _ := agents[agentID].(map[string]any)
	result.Enabled, _ = config["enabled"].(bool)

	setupMode, _ := config["setup_mode"].(string)
	setupMode = strings.ToLower(strings.TrimSpace(setupMode))
	if setupMode != "api_key" {
		return result
	}
	managed, _ := config["managed"].(map[string]any)
	apiKey, _ := managed["api_key"].(string)
	if strings.TrimSpace(apiKey) == "" {
		result.MissingManagedFieldID = "api_key"
	}
	return result
}

func newACPTestService(queries dbstore.Queries) *Service {
	service := NewService(nil, queries, nil)
	service.SetACPSetupValidator(testACPSetupValidator{})
	return service
}

func TestIsKnownTypeIncludesACPAgent(t *testing.T) {
	for _, typ := range []string{TypeChat, TypeSchedule, TypeSubagent, TypeDiscuss, TypeACPAgent} {
		if !IsKnownType(typ) {
			t.Fatalf("IsKnownType(%q) = false", typ)
		}
	}
	if IsKnownType("conversation") {
		t.Fatalf("old conversation-like type should not be accepted")
	}
}

func TestResolveDescriptorRejectsConflictingACPRuntime(t *testing.T) {
	// type=acp_agent unambiguously means an ACP runtime, so an explicit non-ACP
	// runtime_type is contradictory and must error rather than silently
	// downgrade to a plain model chat session.
	if _, _, _, err := ResolveDescriptor(TypeACPAgent, "", RuntimeModel); err == nil {
		t.Fatal("ResolveDescriptor(acp_agent, model) = nil error, want a conflict error")
	} else if !strings.Contains(err.Error(), "conflicts with runtime_type") {
		t.Fatalf("error = %v, want a 'conflicts with runtime_type' message", err)
	}
	// Legitimate combinations must still resolve cleanly.
	cases := []struct {
		name                            string
		legacyType, mode, runtime       string
		wantType, wantMode, wantRuntime string
	}{
		{"acp_agent alone -> chat ACP", TypeACPAgent, "", "", TypeACPAgent, TypeChat, RuntimeACPAgent},
		{"concordant acp_agent+acp_agent", TypeACPAgent, "", RuntimeACPAgent, TypeACPAgent, TypeChat, RuntimeACPAgent},
		{"chat + acp_agent -> chat ACP", TypeChat, "", RuntimeACPAgent, TypeACPAgent, TypeChat, RuntimeACPAgent},
		{"discuss + acp_agent -> discuss ACP", TypeDiscuss, "", RuntimeACPAgent, TypeDiscuss, TypeDiscuss, RuntimeACPAgent},
		{"schedule + acp_agent -> schedule ACP", TypeSchedule, "", RuntimeACPAgent, TypeSchedule, TypeSchedule, RuntimeACPAgent},
		{"plain chat", TypeChat, "", "", TypeChat, TypeChat, RuntimeModel},
	}
	for _, c := range cases {
		gotType, gotMode, gotRuntime, err := ResolveDescriptor(c.legacyType, c.mode, c.runtime)
		if err != nil {
			t.Fatalf("%s: ResolveDescriptor(%q,%q,%q) error = %v", c.name, c.legacyType, c.mode, c.runtime, err)
		}
		if gotType != c.wantType || gotMode != c.wantMode || gotRuntime != c.wantRuntime {
			t.Fatalf("%s: ResolveDescriptor(%q,%q,%q) = (%q,%q,%q), want (%q,%q,%q)",
				c.name, c.legacyType, c.mode, c.runtime, gotType, gotMode, gotRuntime, c.wantType, c.wantMode, c.wantRuntime)
		}
	}
}

func TestValidateACPMetadata(t *testing.T) {
	tests := []struct {
		name    string
		meta    map[string]any
		wantErr string
	}{
		{
			name: "valid acp_agent_id",
			meta: map[string]any{"acp_agent_id": "custom-agent", "project_path": "/data", "runtime_owner_account_id": "owner-1"},
		},
		{
			name:    "missing agent",
			meta:    map[string]any{"project_path": "/data"},
			wantErr: "acp_agent_id is required",
		},
		{
			name:    "missing project path",
			meta:    map[string]any{"acp_agent_id": "custom-agent", "runtime_owner_account_id": "owner-1"},
			wantErr: "project_path is required",
		},
		{
			name:    "missing runtime owner",
			meta:    map[string]any{"acp_agent_id": "custom-agent", "project_path": "/data"},
			wantErr: "runtime_owner_account_id is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateACPMetadata(tc.meta)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateACPMetadata() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateACPMetadata() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateACPCreatePolicy(t *testing.T) {
	botID := mustPGUUID("00000000-0000-0000-0000-000000000001")
	enabledBot := sqlc.GetBotByIDRow{
		ID: botID,
		Metadata: mustSessionJSON(map[string]any{
			"acp": map[string]any{
				"agents": map[string]any{
					"custom-agent": map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
	}
	disabledBot := enabledBot
	disabledBot.Metadata = mustSessionJSON(map[string]any{
		"acp": map[string]any{
			"agents": map[string]any{
				"custom-agent": map[string]any{"enabled": false},
			},
		},
	})

	tests := []struct {
		name     string
		bot      sqlc.GetBotByIDRow
		sessions []sqlc.ListSessionsByBotRow
		meta     map[string]any
		wantErr  string
	}{
		{
			name: "enabled",
			bot:  enabledBot,
			meta: map[string]any{"acp_agent_id": "custom-agent", "project_path": "/data"},
		},
		{
			name: "enabled but missing managed configuration",
			bot: sqlc.GetBotByIDRow{
				ID: botID,
				Metadata: mustSessionJSON(map[string]any{
					"acp": map[string]any{
						"agents": map[string]any{
							"custom-agent": map[string]any{"enabled": true, "setup_mode": "api_key"},
						},
					},
				}),
			},
			meta:    map[string]any{"acp_agent_id": "custom-agent", "project_path": "/data"},
			wantErr: "is not configured",
		},
		{
			name:    "unknown agent",
			bot:     enabledBot,
			meta:    map[string]any{"acp_agent_id": "unknown", "project_path": "/data"},
			wantErr: "unknown ACP agent",
		},
		{
			name:    "bot disabled",
			bot:     disabledBot,
			meta:    map[string]any{"acp_agent_id": "custom-agent", "project_path": "/data"},
			wantErr: "is not enabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newACPTestService(acpPolicyQueries{
				bot:      tt.bot,
				sessions: tt.sessions,
			})
			err := svc.validateACPCreatePolicy(context.Background(), botID, tt.meta)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateACPCreatePolicy() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateACPCreatePolicy() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestCreateACPAgentSessionRunsValidationAndPersistsType(t *testing.T) {
	botID := "00000000-0000-0000-0000-000000000001"
	botUUID := mustPGUUID(botID)
	queries := &createACPQueries{
		bot: sqlc.GetBotByIDRow{
			ID: botUUID,
			Metadata: mustSessionJSON(map[string]any{
				"acp": map[string]any{
					"agents": map[string]any{
						"custom-agent": map[string]any{"enabled": true, "setup_mode": "self"},
					},
				},
			}),
		},
	}
	svc := newACPTestService(queries)

	created, err := svc.Create(context.Background(), CreateInput{
		BotID:           botID,
		Type:            TypeACPAgent,
		Title:           "Codex",
		CreatedByUserID: "00000000-0000-0000-0000-000000000003",
		Metadata: map[string]any{
			"acp_agent_id":             "custom-agent",
			"project_path":             "/data/app",
			"runtime_owner_account_id": "00000000-0000-0000-0000-000000000099",
		},
	})
	if err != nil {
		t.Fatalf("Create(acp_agent) error = %v", err)
	}
	if queries.createParams.Type != TypeACPAgent {
		t.Fatalf("CreateSession type = %q, want %q", queries.createParams.Type, TypeACPAgent)
	}
	if created.Type != TypeACPAgent {
		t.Fatalf("created type = %q, want %q", created.Type, TypeACPAgent)
	}
	if queries.createParams.SessionMode != TypeChat || queries.createParams.RuntimeType != RuntimeACPAgent {
		t.Fatalf("CreateSession descriptor = %q/%q, want chat/acp_agent", queries.createParams.SessionMode, queries.createParams.RuntimeType)
	}
	if created.SessionMode != TypeChat || created.RuntimeType != RuntimeACPAgent {
		t.Fatalf("created descriptor = %q/%q, want chat/acp_agent", created.SessionMode, created.RuntimeType)
	}
	if got := created.Metadata["acp_agent_id"]; got != "custom-agent" {
		t.Fatalf("created metadata acp_agent_id = %#v", got)
	}
	if got := created.RuntimeMetadata["acp_agent_id"]; got != "custom-agent" {
		t.Fatalf("created runtime metadata acp_agent_id = %#v", got)
	}
	if got := created.RuntimeMetadata["runtime_owner_account_id"]; got != "00000000-0000-0000-0000-000000000003" {
		t.Fatalf("created runtime owner = %#v, want server owner", got)
	}
}

func TestCreateACPAgentSessionDefaultsProjectPath(t *testing.T) {
	botID := "00000000-0000-0000-0000-000000000001"
	botUUID := mustPGUUID(botID)
	queries := &createACPQueries{
		bot: sqlc.GetBotByIDRow{
			ID: botUUID,
			Metadata: mustSessionJSON(map[string]any{
				"acp": map[string]any{
					"agents": map[string]any{
						"custom-agent": map[string]any{"enabled": true, "setup_mode": "self"},
					},
				},
			}),
		},
	}
	svc := newACPTestService(queries)

	created, err := svc.Create(context.Background(), CreateInput{
		BotID:           botID,
		Type:            TypeACPAgent,
		Title:           "Codex",
		CreatedByUserID: "00000000-0000-0000-0000-000000000003",
		Metadata: map[string]any{
			"acp_agent_id": "custom-agent",
		},
	})
	if err != nil {
		t.Fatalf("Create(acp_agent) error = %v", err)
	}
	if created.Metadata["project_path"] != DefaultACPProjectPath {
		t.Fatalf("created metadata project_path = %#v, want %q", created.Metadata["project_path"], DefaultACPProjectPath)
	}
	if created.Metadata["acp_project_mode"] != DefaultACPProjectMode {
		t.Fatalf("created metadata acp_project_mode = %#v, want %q", created.Metadata["acp_project_mode"], DefaultACPProjectMode)
	}
}

func TestUpdateTypeAndMetadataACPAgentRunsPolicy(t *testing.T) {
	botID := "00000000-0000-0000-0000-000000000001"
	sessionID := "00000000-0000-0000-0000-000000000002"
	botUUID := mustPGUUID(botID)
	sessionUUID := mustPGUUID(sessionID)
	queries := &updateACPQueries{
		bot: sqlc.GetBotByIDRow{
			ID: botUUID,
			Metadata: mustSessionJSON(map[string]any{
				"acp": map[string]any{
					"agents": map[string]any{
						"custom-agent": map[string]any{"enabled": true, "setup_mode": "self"},
					},
				},
			}),
		},
		session: sqlc.BotSession{
			ID:    sessionUUID,
			BotID: botUUID,
			Type:  TypeACPAgent,
		},
	}
	svc := newACPTestService(queries)

	updated, err := svc.UpdateTypeAndMetadataWithOwner(context.Background(), sessionID, TypeACPAgent, map[string]any{
		"acp_agent_id":             "custom-agent",
		"project_path":             "/data/app",
		"runtime_owner_account_id": "00000000-0000-0000-0000-000000000099",
	}, "00000000-0000-0000-0000-000000000003")
	if err != nil {
		t.Fatalf("UpdateTypeAndMetadata(acp_agent) error = %v", err)
	}
	if queries.updateParams.Type != TypeACPAgent {
		t.Fatalf("UpdateSessionTypeAndMetadata type = %q, want %q", queries.updateParams.Type, TypeACPAgent)
	}
	if updated.Type != TypeACPAgent {
		t.Fatalf("updated type = %q, want %q", updated.Type, TypeACPAgent)
	}
	if queries.updateParams.SessionMode != TypeChat || queries.updateParams.RuntimeType != RuntimeACPAgent {
		t.Fatalf("UpdateSessionTypeAndMetadata descriptor = %q/%q, want chat/acp_agent", queries.updateParams.SessionMode, queries.updateParams.RuntimeType)
	}
	if updated.SessionMode != TypeChat || updated.RuntimeType != RuntimeACPAgent {
		t.Fatalf("updated descriptor = %q/%q, want chat/acp_agent", updated.SessionMode, updated.RuntimeType)
	}
	if updated.RuntimeMetadata["runtime_owner_account_id"] != "00000000-0000-0000-0000-000000000003" {
		t.Fatalf("updated runtime owner = %#v, want server owner", updated.RuntimeMetadata["runtime_owner_account_id"])
	}
}

func TestCreatePersistsCanonicalChannelType(t *testing.T) {
	t.Parallel()

	// Local product surfaces (web composer, bundled CLI) must persist the
	// canonical "local" channel type: the web UI renders any other non-empty
	// channel_type as a read-only external channel thread, which is how
	// WS-created skill-activation sessions lost their composer after refresh.
	cases := []struct {
		in   string
		want string
	}{
		{in: "web", want: "local"},
		{in: "cli", want: "local"},
		{in: " Web ", want: "local"},
		{in: "local", want: "local"},
		{in: "telegram", want: "telegram"},
		{in: "", want: ""},
	}
	for _, tc := range cases {
		queries := &createACPQueries{}
		svc := NewService(nil, queries, nil)
		created, err := svc.Create(context.Background(), CreateInput{
			BotID:       "00000000-0000-0000-0000-000000000001",
			ChannelType: tc.in,
			Type:        TypeChat,
		})
		if err != nil {
			t.Fatalf("Create(channel_type=%q) error = %v", tc.in, err)
		}
		got := ""
		if queries.createParams.ChannelType.Valid {
			got = queries.createParams.ChannelType.String
		}
		if got != tc.want {
			t.Fatalf("persisted channel_type for input %q = %q, want %q", tc.in, got, tc.want)
		}
		if created.ChannelType != tc.want {
			t.Fatalf("created.ChannelType for input %q = %q, want %q", tc.in, created.ChannelType, tc.want)
		}
	}
}

func TestNormalizeDescriptorAllowsDiscussACP(t *testing.T) {
	t.Parallel()

	desc, err := normalizeDescriptor(TypeDiscuss, TypeDiscuss, RuntimeACPAgent, map[string]any{
		"acp_agent_id": "custom-agent",
		"project_path": "/data/group",
	}, nil)
	if err != nil {
		t.Fatalf("normalizeDescriptor(discuss/acp) error = %v", err)
	}
	if desc.LegacyType != TypeDiscuss || desc.SessionMode != TypeDiscuss || desc.RuntimeType != RuntimeACPAgent {
		t.Fatalf("descriptor = %#v, want legacy discuss, mode discuss, runtime acp_agent", desc)
	}
}

type acpPolicyQueries struct {
	dbstore.Queries
	bot      sqlc.GetBotByIDRow
	sessions []sqlc.ListSessionsByBotRow
}

func (q acpPolicyQueries) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (q acpPolicyQueries) ListSessionsByBot(context.Context, pgtype.UUID) ([]sqlc.ListSessionsByBotRow, error) {
	return q.sessions, nil
}

type createACPQueries struct {
	dbstore.Queries
	bot          sqlc.GetBotByIDRow
	sessions     []sqlc.ListSessionsByBotRow
	createParams sqlc.CreateSessionParams
}

func (q *createACPQueries) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (q *createACPQueries) ListSessionsByBot(context.Context, pgtype.UUID) ([]sqlc.ListSessionsByBotRow, error) {
	return q.sessions, nil
}

func (q *createACPQueries) CreateSession(_ context.Context, params sqlc.CreateSessionParams) (sqlc.BotSession, error) {
	q.createParams = params
	return sqlc.BotSession{
		ID:              mustPGUUID("00000000-0000-0000-0000-000000000002"),
		BotID:           params.BotID,
		RouteID:         params.RouteID,
		ChannelType:     params.ChannelType,
		Type:            params.Type,
		SessionMode:     params.SessionMode,
		RuntimeType:     params.RuntimeType,
		RuntimeMetadata: params.RuntimeMetadata,
		Title:           params.Title,
		Metadata:        params.Metadata,
		ParentSessionID: params.ParentSessionID,
		CreatedByUserID: params.CreatedByUserID,
	}, nil
}

type updateACPQueries struct {
	dbstore.Queries
	bot          sqlc.GetBotByIDRow
	session      sqlc.BotSession
	sessions     []sqlc.ListSessionsByBotRow
	updateParams sqlc.UpdateSessionTypeAndMetadataParams
}

func (q *updateACPQueries) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (q *updateACPQueries) GetSessionByID(context.Context, pgtype.UUID) (sqlc.BotSession, error) {
	return q.session, nil
}

func (q *updateACPQueries) ListSessionsByBot(context.Context, pgtype.UUID) ([]sqlc.ListSessionsByBotRow, error) {
	return q.sessions, nil
}

func (q *updateACPQueries) UpdateSessionTypeAndMetadata(_ context.Context, params sqlc.UpdateSessionTypeAndMetadataParams) (sqlc.BotSession, error) {
	q.updateParams = params
	updated := q.session
	updated.Type = params.Type
	updated.SessionMode = params.SessionMode
	updated.RuntimeType = params.RuntimeType
	updated.RuntimeMetadata = params.RuntimeMetadata
	updated.Metadata = params.Metadata
	return updated, nil
}

func mustPGUUID(value string) pgtype.UUID {
	var out pgtype.UUID
	if err := out.Scan(value); err != nil {
		panic(err)
	}
	return out
}

func mustSessionJSON(value map[string]any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

// pagedQueriesStub captures the params handed to the paged session queries so
// the test can assert that the service forwards botID, types, cursor, and
// limit without modification.
type pagedQueriesStub struct {
	dbstore.Queries
	pagedArg     sqlc.ListSessionsByBotPagedParams
	pagedRows    []sqlc.ListSessionsByBotPagedRow
	userPagedArg sqlc.ListSessionsByBotAndCreatedByUserPagedParams
	userRows     []sqlc.ListSessionsByBotAndCreatedByUserPagedRow
}

func (s *pagedQueriesStub) ListSessionsByBotPaged(_ context.Context, arg sqlc.ListSessionsByBotPagedParams) ([]sqlc.ListSessionsByBotPagedRow, error) {
	s.pagedArg = arg
	return s.pagedRows, nil
}

func (s *pagedQueriesStub) ListSessionsByBotAndCreatedByUserPaged(_ context.Context, arg sqlc.ListSessionsByBotAndCreatedByUserPagedParams) ([]sqlc.ListSessionsByBotAndCreatedByUserPagedRow, error) {
	s.userPagedArg = arg
	return s.userRows, nil
}

func TestListByBotPagedForwardsParams(t *testing.T) {
	stub := &pagedQueriesStub{}
	svc := NewService(nil, stub, nil)
	botID := "11111111-1111-1111-1111-111111111111"
	cursorID := "22222222-2222-2222-2222-222222222222"
	cursorAt := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	types := []string{TypeChat, TypeDiscuss}

	if _, err := svc.ListByBotPaged(context.Background(), botID, types, Cursor{UpdatedAt: cursorAt, ID: cursorID}, 25); err != nil {
		t.Fatalf("ListByBotPaged: %v", err)
	}
	if stub.pagedArg.BotID.String() != botID {
		t.Fatalf("BotID = %s, want %s", stub.pagedArg.BotID.String(), botID)
	}
	if got, want := stub.pagedArg.Types, types; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Types = %v, want %v", got, want)
	}
	if !stub.pagedArg.UseCursor {
		t.Fatalf("UseCursor should be true when a non-zero cursor is supplied")
	}
	if !stub.pagedArg.CursorUpdatedAt.Time.Equal(cursorAt) {
		t.Fatalf("CursorUpdatedAt = %v, want %v", stub.pagedArg.CursorUpdatedAt.Time, cursorAt)
	}
	if stub.pagedArg.CursorID.String() != cursorID {
		t.Fatalf("CursorID = %s, want %s", stub.pagedArg.CursorID.String(), cursorID)
	}
	if stub.pagedArg.LimitCount != 25 {
		t.Fatalf("LimitCount = %d, want 25", stub.pagedArg.LimitCount)
	}
}

func TestListByBotPagedWithFilterForwardsParentThreadID(t *testing.T) {
	stub := &pagedQueriesStub{}
	svc := NewService(nil, stub, nil)
	botID := "11111111-1111-1111-1111-111111111111"
	parentID := "22222222-2222-2222-2222-222222222222"

	if _, err := svc.ListByBotPagedWithFilter(context.Background(), botID, []string{TypeSubagent}, Cursor{}, 25, ListFilter{
		ParentThreadID: parentID,
	}); err != nil {
		t.Fatalf("ListByBotPagedWithFilter: %v", err)
	}
	if !stub.pagedArg.UseParentSession {
		t.Fatalf("UseParentSession should be true when a parent session filter is supplied")
	}
	if stub.pagedArg.ParentSessionID.String() != parentID {
		t.Fatalf("ParentSessionID = %s, want %s", stub.pagedArg.ParentSessionID.String(), parentID)
	}
}

func TestListByBotPagedZeroCursorSkipsCursorFilter(t *testing.T) {
	stub := &pagedQueriesStub{}
	svc := NewService(nil, stub, nil)

	if _, err := svc.ListByBotPaged(context.Background(), "11111111-1111-1111-1111-111111111111", []string{TypeChat}, Cursor{}, 10); err != nil {
		t.Fatalf("ListByBotPaged: %v", err)
	}
	if stub.pagedArg.UseCursor {
		t.Fatalf("UseCursor should be false for the zero-value cursor")
	}
}

func TestListByBotPagedMapsRowsToSessions(t *testing.T) {
	rowID := "33333333-3333-3333-3333-333333333333"
	updated := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	stub := &pagedQueriesStub{
		pagedRows: []sqlc.ListSessionsByBotPagedRow{{
			ID:        mustPGUUID(rowID),
			BotID:     mustPGUUID("11111111-1111-1111-1111-111111111111"),
			Type:      TypeChat,
			Title:     "hello",
			Metadata:  []byte(`{"k":"v"}`),
			CreatedAt: pgtype.Timestamptz{Time: updated, Valid: true},
			UpdatedAt: pgtype.Timestamptz{Time: updated, Valid: true},
		}},
	}
	svc := NewService(nil, stub, nil)

	got, err := svc.ListByBotPaged(context.Background(), "11111111-1111-1111-1111-111111111111", []string{TypeChat}, Cursor{}, 10)
	if err != nil {
		t.Fatalf("ListByBotPaged: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	if got[0].ID != rowID {
		t.Fatalf("session ID = %s, want %s", got[0].ID, rowID)
	}
	if !got[0].UpdatedAt.Equal(updated) {
		t.Fatalf("session UpdatedAt = %v, want %v", got[0].UpdatedAt, updated)
	}
	if got[0].Metadata["k"] != "v" {
		t.Fatalf("session metadata = %v, want k=v", got[0].Metadata)
	}
}

func TestListByBotAndCreatedByUserPagedForwardsUserScope(t *testing.T) {
	stub := &pagedQueriesStub{}
	svc := NewService(nil, stub, nil)
	botID := "11111111-1111-1111-1111-111111111111"
	userID := "44444444-4444-4444-4444-444444444444"

	if _, err := svc.ListByBotAndCreatedByUserPaged(context.Background(), botID, userID, []string{TypeChat}, Cursor{}, 5); err != nil {
		t.Fatalf("ListByBotAndCreatedByUserPaged: %v", err)
	}
	if stub.userPagedArg.BotID.String() != botID {
		t.Fatalf("BotID = %s, want %s", stub.userPagedArg.BotID.String(), botID)
	}
	if stub.userPagedArg.CreatedByUserID.String() != userID {
		t.Fatalf("CreatedByUserID = %s, want %s", stub.userPagedArg.CreatedByUserID.String(), userID)
	}
	if stub.userPagedArg.LimitCount != 5 {
		t.Fatalf("LimitCount = %d, want 5", stub.userPagedArg.LimitCount)
	}
}

// TestCursorIsZeroDistinguishesPartialFromEmpty pins down the contract
// that pagedCursorParams relies on: a partial cursor (only one half set) is
// not zero, so a misconstructed cursor surfaces as an error rather than
// silently restarting the listing from the head.
func TestCursorIsZeroDistinguishesPartialFromEmpty(t *testing.T) {
	if !(Cursor{}).IsZero() {
		t.Fatalf("zero-value cursor should be zero")
	}
	partialID := Cursor{ID: "33333333-3333-3333-3333-333333333333"}
	if partialID.IsZero() {
		t.Fatalf("cursor with only id set should not be zero")
	}
	partialTS := Cursor{UpdatedAt: time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)}
	if partialTS.IsZero() {
		t.Fatalf("cursor with only updated_at set should not be zero")
	}
}

func TestListByBotPagedRejectsPartialCursor(t *testing.T) {
	stub := &pagedQueriesStub{}
	svc := NewService(nil, stub, nil)
	botID := "11111111-1111-1111-1111-111111111111"

	_, err := svc.ListByBotPaged(context.Background(), botID, []string{TypeChat},
		Cursor{ID: "33333333-3333-3333-3333-333333333333"}, 10)
	if err == nil {
		t.Fatalf("ListByBotPaged with id-only cursor should error rather than restart from the head")
	}
	if !strings.Contains(err.Error(), "cursor must carry both") {
		t.Fatalf("error = %v, want partial-cursor message", err)
	}
}

func TestThreadVisibilityIsDerivedFromMode(t *testing.T) {
	tests := []struct {
		name       string
		legacyType string
		mode       string
		want       Visibility
	}{
		{name: "chat", legacyType: TypeChat, mode: TypeChat, want: VisibilityUser},
		{name: "discuss", legacyType: TypeDiscuss, mode: TypeDiscuss, want: VisibilityUser},
		{name: "legacy acp", legacyType: TypeACPAgent, want: VisibilityUser},
		{name: "schedule", legacyType: TypeSchedule, mode: TypeSchedule, want: VisibilityInternal},
		{name: "subagent", legacyType: TypeSubagent, mode: TypeSubagent, want: VisibilityInternal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toThread(sqlc.BotSession{
				Type:        tt.legacyType,
				SessionMode: tt.mode,
			})
			if got.Visibility != tt.want {
				t.Fatalf("Visibility = %q, want %q", got.Visibility, tt.want)
			}
		})
	}
}

func TestThreadJSONKeepsLegacySessionFieldNames(t *testing.T) {
	data, err := json.Marshal(Thread{
		ID:             "thread-1",
		ParentThreadID: "thread-parent",
		Visibility:     VisibilityInternal,
	})
	if err != nil {
		t.Fatalf("marshal thread: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, `"parent_session_id":"thread-parent"`) {
		t.Fatalf("JSON = %s, want parent_session_id", got)
	}
	if strings.Contains(got, "parent_thread_id") || strings.Contains(got, "visibility") {
		t.Fatalf("JSON exposed internal thread fields: %s", got)
	}
}
