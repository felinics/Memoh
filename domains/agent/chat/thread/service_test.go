package thread

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeThreadStore struct {
	Store
	created CreateRecord
	listed  ListRecord
	thread  Thread
}

func (f *fakeThreadStore) InTransaction(_ context.Context, fn func(Store) error) error {
	return fn(f)
}

func (f *fakeThreadStore) CreateThread(_ context.Context, record CreateRecord) (Thread, error) {
	f.created = record
	return Thread{
		ID: "00000000-0000-0000-0000-000000000002", BotID: record.BotID,
		ChannelType: record.ChannelType, Type: record.Type, SessionMode: record.SessionMode,
		RuntimeType: record.RuntimeType, RuntimeMetadata: record.RuntimeMetadata,
		ConversationType: record.ConversationType, ConversationName: record.ConversationName,
		ReplyTarget: record.ReplyTarget,
		Title:       record.Title, Metadata: record.Metadata, ParentThreadID: record.ParentThreadID,
		CreatedByUserID: record.CreatedByUserID,
	}, nil
}

func TestCreateCarriesRouteSnapshot(t *testing.T) {
	store := &fakeThreadStore{}
	created, err := NewService(nil, store, nil, nil).Create(t.Context(), CreateInput{
		BotID:            "00000000-0000-0000-0000-000000000001",
		RouteID:          "00000000-0000-0000-0000-000000000002",
		ConversationType: "group",
		ConversationName: " Memoh ",
		ReplyTarget:      " chat-1 ",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if store.created.ConversationType != "group" ||
		store.created.ConversationName != "Memoh" ||
		store.created.ReplyTarget != "chat-1" {
		t.Fatalf("created record = %#v", store.created)
	}
	if created.ConversationName != "Memoh" {
		t.Fatalf("created thread = %#v", created)
	}
}

func (f *fakeThreadStore) GetThread(context.Context, string) (Thread, error) {
	return f.thread, nil
}

func (f *fakeThreadStore) ListThreadsByBot(_ context.Context, record ListRecord) ([]Thread, error) {
	f.listed = record
	return []Thread{f.thread}, nil
}

func (f *fakeThreadStore) ListThreadsByUser(_ context.Context, record ListRecord) ([]Thread, error) {
	f.listed = record
	return []Thread{f.thread}, nil
}

type fakePolicyReader struct {
	metadata map[string]any
}

func (f fakePolicyReader) GetBotMetadata(context.Context, string) (map[string]any, error) {
	return f.metadata, nil
}

type testACPSetupValidator struct{}

func (testACPSetupValidator) ValidateACPSetup(agentID string, metadata map[string]any) ACPSetupValidation {
	if strings.ToLower(strings.TrimSpace(agentID)) != "codex" {
		return ACPSetupValidation{}
	}
	result := ACPSetupValidation{Known: true}
	acp, _ := metadata["acp"].(map[string]any)
	agents, _ := acp["agents"].(map[string]any)
	config, _ := agents["codex"].(map[string]any)
	result.Enabled, _ = config["enabled"].(bool)
	return result
}

func TestResolveDescriptor(t *testing.T) {
	tests := []struct {
		legacy, mode, runtime             string
		wantLegacy, wantMode, wantRuntime string
	}{
		{TypeACPAgent, "", "", TypeACPAgent, TypeChat, RuntimeACPAgent},
		{TypeDiscuss, TypeDiscuss, RuntimeACPAgent, TypeDiscuss, TypeDiscuss, RuntimeACPAgent},
		{TypeChat, "", "", TypeChat, TypeChat, RuntimeModel},
	}
	for _, test := range tests {
		legacy, mode, runtime, err := ResolveDescriptor(test.legacy, test.mode, test.runtime)
		if err != nil {
			t.Fatal(err)
		}
		if legacy != test.wantLegacy || mode != test.wantMode || runtime != test.wantRuntime {
			t.Fatalf("descriptor = %q/%q/%q", legacy, mode, runtime)
		}
	}
	if _, _, _, err := ResolveDescriptor(TypeACPAgent, "", RuntimeModel); err == nil {
		t.Fatal("expected contradictory ACP descriptor error")
	}
}

func TestCreateACPThreadUsesPolicyAndCanonicalChannel(t *testing.T) {
	store := &fakeThreadStore{}
	policy := fakePolicyReader{metadata: map[string]any{
		"acp": map[string]any{"agents": map[string]any{"codex": map[string]any{"enabled": true}}},
	}}
	svc := NewService(nil, store, policy, nil)
	svc.SetACPSetupValidator(testACPSetupValidator{})
	created, err := svc.Create(t.Context(), CreateInput{
		BotID: "00000000-0000-0000-0000-000000000001", ChannelType: "web",
		Type: TypeACPAgent, CreatedByUserID: "00000000-0000-0000-0000-000000000003",
		Metadata: map[string]any{"acp_agent_id": "codex"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if store.created.ChannelType != "local" || created.RuntimeType != RuntimeACPAgent {
		t.Fatalf("created = %+v, record = %+v", created, store.created)
	}
	if created.Metadata["project_path"] != DefaultACPProjectPath ||
		created.RuntimeMetadata["runtime_owner_account_id"] != "00000000-0000-0000-0000-000000000003" {
		t.Fatalf("ACP defaults = %+v / %+v", created.Metadata, created.RuntimeMetadata)
	}
}

func TestListByBotPagedBuildsDomainQuery(t *testing.T) {
	store := &fakeThreadStore{thread: Thread{ID: "thread"}}
	svc := NewService(nil, store, nil, nil)
	cursorAt := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	_, err := svc.ListByBotPagedWithFilter(t.Context(),
		"11111111-1111-1111-1111-111111111111", []string{TypeChat},
		Cursor{UpdatedAt: cursorAt, ID: "22222222-2222-2222-2222-222222222222"},
		25, ListFilter{ParentThreadID: "33333333-3333-3333-3333-333333333333"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.listed.UseCursor || !store.listed.UseParent || store.listed.Limit != 25 {
		t.Fatalf("list record = %+v", store.listed)
	}
	if _, err := svc.ListByBotPaged(t.Context(), "11111111-1111-1111-1111-111111111111",
		[]string{TypeChat}, Cursor{ID: "22222222-2222-2222-2222-222222222222"}, 10); err == nil {
		t.Fatal("partial cursor should fail")
	}
}

func TestThreadJSONKeepsLegacySessionFieldNames(t *testing.T) {
	data, err := json.Marshal(Thread{
		ID: "thread-1", ParentThreadID: "thread-parent", Visibility: VisibilityInternal,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, `"parent_session_id":"thread-parent"`) ||
		strings.Contains(got, "parent_thread_id") || strings.Contains(got, "visibility") {
		t.Fatalf("JSON = %s", got)
	}
}

// TestValidateACPMetadata restores coverage lost in the domains restructure.
// ACP threads carry the agent id, project path, and runtime owner that the
// runtime later trusts, so a missing field must fail at creation rather than
// surface as a broken session.
func TestValidateACPMetadata(t *testing.T) {
	tests := []struct {
		name    string
		meta    map[string]any
		wantErr error
	}{
		{
			name: "complete metadata",
			meta: map[string]any{"acp_agent_id": "codex", "project_path": "/data", "runtime_owner_account_id": "owner-1"},
		},
		{
			name:    "missing agent id",
			meta:    map[string]any{"project_path": "/data", "runtime_owner_account_id": "owner-1"},
			wantErr: ErrACPAgentIDRequired,
		},
		{
			name:    "missing project path",
			meta:    map[string]any{"acp_agent_id": "codex", "runtime_owner_account_id": "owner-1"},
			wantErr: ErrACPProjectPathMissing,
		},
		{
			name:    "missing runtime owner",
			meta:    map[string]any{"acp_agent_id": "codex", "project_path": "/data"},
			wantErr: ErrACPRuntimeOwnerMissing,
		},
		{
			name: "explicit project mode is accepted",
			meta: map[string]any{
				"acp_agent_id": "codex", "project_path": "/data",
				"runtime_owner_account_id": "owner-1", "acp_project_mode": DefaultACPProjectMode,
			},
		},
		{
			name: "none project mode is accepted",
			meta: map[string]any{
				"acp_agent_id": "codex", "project_path": "/data",
				"runtime_owner_account_id": "owner-1", "acp_project_mode": "none",
			},
		},
		{
			name: "unknown project mode is rejected",
			meta: map[string]any{
				"acp_agent_id": "codex", "project_path": "/data",
				"runtime_owner_account_id": "owner-1", "acp_project_mode": "sandbox",
			},
			wantErr: ErrACPProjectModeInvalid,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateACPMetadata(tc.meta)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("validateACPMetadata() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("validateACPMetadata() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestUpdateTypeAndMetadataACPAgentRunsPolicy restores coverage lost in the
// restructure: promoting a thread to acp_agent must run the same metadata
// policy as creation, otherwise the validation is trivially bypassed by
// creating a chat thread and updating its type afterwards.
func TestUpdateTypeAndMetadataACPAgentRunsPolicy(t *testing.T) {
	store := &fakeThreadStore{thread: Thread{
		ID:          "00000000-0000-0000-0000-000000000002",
		BotID:       "00000000-0000-0000-0000-000000000001",
		Type:        TypeChat,
		ChannelType: "web",
	}}

	_, err := NewService(nil, store, nil, nil).UpdateTypeAndMetadata(
		t.Context(),
		"00000000-0000-0000-0000-000000000002",
		TypeACPAgent,
		map[string]any{"project_path": "/data", "runtime_owner_account_id": "owner-1"},
	)
	if !errors.Is(err, ErrACPAgentIDRequired) {
		t.Fatalf("UpdateTypeAndMetadata() error = %v, want %v", err, ErrACPAgentIDRequired)
	}
}
