package workspacecontext

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	"github.com/memohai/memoh/internal/hooks"
	"github.com/memohai/memoh/internal/skills"
	workspacepkg "github.com/memohai/memoh/internal/workspace"
)

const workspaceContextTestBotID = "6e33a6ad-f888-4051-9b1a-e709bdc048b2"

type cachedSnapshotStore struct {
	row      sqlc.BotWorkspaceContextSnapshot
	rows     map[string]sqlc.BotWorkspaceContextSnapshot
	getCalls int
}

type sequenceSnapshotStore struct {
	cachedSnapshotStore
	rows []sqlc.BotWorkspaceContextSnapshot
}

func (s *sequenceSnapshotStore) GetBotWorkspaceContextSnapshot(_ context.Context, _ sqlc.GetBotWorkspaceContextSnapshotParams) (sqlc.BotWorkspaceContextSnapshot, error) {
	s.getCalls++
	index := s.getCalls - 1
	if index >= len(s.rows) {
		index = len(s.rows) - 1
	}
	return s.rows[index], nil
}

func (s *cachedSnapshotStore) GetBotWorkspaceContextSnapshot(_ context.Context, arg sqlc.GetBotWorkspaceContextSnapshotParams) (sqlc.BotWorkspaceContextSnapshot, error) {
	s.getCalls++
	if s.rows != nil {
		row, ok := s.rows[arg.TargetID]
		if !ok {
			return sqlc.BotWorkspaceContextSnapshot{}, errors.New("unexpected target")
		}
		return row, nil
	}
	return s.row, nil
}

func (*cachedSnapshotStore) BeginBotWorkspaceContextRefresh(context.Context, sqlc.BeginBotWorkspaceContextRefreshParams) (int64, error) {
	return 0, errors.New("unexpected refresh")
}

func (*cachedSnapshotStore) CompleteBotWorkspaceContextRefresh(context.Context, sqlc.CompleteBotWorkspaceContextRefreshParams) (sqlc.BotWorkspaceContextSnapshot, error) {
	return sqlc.BotWorkspaceContextSnapshot{}, errors.New("unexpected refresh")
}

func (*cachedSnapshotStore) MarkBotWorkspaceContextSourceInvalid(context.Context, sqlc.MarkBotWorkspaceContextSourceInvalidParams) (int64, error) {
	return 0, errors.New("unexpected refresh")
}

func (*cachedSnapshotStore) FailBotWorkspaceContextRefresh(context.Context, sqlc.FailBotWorkspaceContextRefreshParams) (int64, error) {
	return 0, errors.New("unexpected refresh")
}

func TestCachedSnapshotServesHooksSkillsAndFilesWithoutWorkspace(t *testing.T) {
	rawHooks := `{
		"version": 1,
		"enabled": true,
		"hooks": [{
			"name": "cached",
			"event": "BeforePromptBuild",
			"actions": [{"type": "command", "command": "true"}]
		}]
	}`
	payload := Payload{
		Version: 1,
		HookDocuments: []HookDocument{{
			Kind:   "user",
			Path:   hooks.DefaultConfigPath,
			Raw:    rawHooks,
			Exists: true,
		}},
		Skills: []skills.Entry{{
			Name:                    "cached-skill",
			Description:             "loaded from PostgreSQL",
			Content:                 "cached content",
			State:                   skills.StateEffective,
			RuntimeUsable:           true,
			RuntimeUsabilityChecked: true,
		}},
		SystemFiles: []SystemFile{{Filename: "AGENTS.md", Content: "cached instructions"}},
		Heartbeat:   "cached checklist",
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	botUUID := uuid.MustParse(workspaceContextTestBotID)
	store := &cachedSnapshotStore{row: sqlc.BotWorkspaceContextSnapshot{
		BotID:               pgtype.UUID{Bytes: botUUID, Valid: true},
		TargetID:            "native",
		RequestedGeneration: 4,
		AppliedGeneration:   4,
		Status:              StatusReady,
		Payload:             rawPayload,
		ContentHash:         pgtype.Text{String: "hash", Valid: true},
	}}
	service := NewService(nil, store, nil, nil)

	ctx, snapshot, err := service.Attach(t.Context(), workspaceContextTestBotID)
	if err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	if snapshot.AppliedGeneration != 4 {
		t.Fatalf("snapshot generation = %d, want 4", snapshot.AppliedGeneration)
	}

	cfg, exists, err := service.LoadEffectiveHooks(ctx, workspaceContextTestBotID)
	if err != nil {
		t.Fatalf("LoadEffectiveHooks() error = %v", err)
	}
	if !exists || len(cfg.Hooks) != 1 || cfg.Hooks[0].Name != "cached" {
		t.Fatalf("cached hooks = %#v, exists = %v", cfg.Hooks, exists)
	}
	gotSkills, err := service.Skills(ctx, workspaceContextTestBotID, true)
	if err != nil {
		t.Fatalf("Skills() error = %v", err)
	}
	if len(gotSkills) != 1 || gotSkills[0].Name != "cached-skill" {
		t.Fatalf("cached skills = %#v", gotSkills)
	}
	files, err := service.SystemFiles(ctx, workspaceContextTestBotID)
	if err != nil {
		t.Fatalf("SystemFiles() error = %v", err)
	}
	if len(files) != 1 || files[0].Content != "cached instructions" {
		t.Fatalf("cached files = %#v", files)
	}
	checklist, err := service.HeartbeatChecklist(ctx, workspaceContextTestBotID)
	if err != nil {
		t.Fatalf("HeartbeatChecklist() error = %v", err)
	}
	if checklist != "cached checklist" {
		t.Fatalf("cached checklist = %q", checklist)
	}
	if store.getCalls != 1 {
		t.Fatalf("database reads = %d, want one request-scoped read", store.getCalls)
	}
}

func TestIsRelevantPath(t *testing.T) {
	for _, filePath := range []string{
		"/data/.memoh/hooks.json",
		"/data/AGENTS.md",
		"/data/HEARTBEAT.md",
		"/data/MEMORY.md",
		"/data/PROFILES.md",
		"/data/skills/example/SKILL.md",
		"/data/.memoh/plugins/example/hooks.json",
		"/data/.memoh",
		"/data",
	} {
		if !IsRelevantPath(filePath) {
			t.Errorf("IsRelevantPath(%q) = false", filePath)
		}
	}
	if IsRelevantPath("/data/notes.txt") {
		t.Fatal("ordinary workspace file should not be a direct invalidation path")
	}
}

func TestCachedSnapshotsAreScopedByWorkspaceTarget(t *testing.T) {
	botUUID := uuid.MustParse(workspaceContextTestBotID)
	makeRow := func(targetID, content string) sqlc.BotWorkspaceContextSnapshot {
		t.Helper()
		raw, err := json.Marshal(Payload{
			Version:     1,
			SystemFiles: []SystemFile{{Filename: "AGENTS.md", Content: content}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return sqlc.BotWorkspaceContextSnapshot{
			BotID:               pgtype.UUID{Bytes: botUUID, Valid: true},
			TargetID:            targetID,
			RequestedGeneration: 1,
			AppliedGeneration:   1,
			Status:              StatusReady,
			Payload:             raw,
			ContentHash:         pgtype.Text{String: "hash", Valid: true},
		}
	}
	store := &cachedSnapshotStore{rows: map[string]sqlc.BotWorkspaceContextSnapshot{
		"native":     makeRow("native", "server instructions"),
		"computer-b": makeRow("computer-b", "computer instructions"),
	}}
	service := NewService(nil, store, nil, nil)

	for targetID, want := range map[string]string{
		"native":     "server instructions",
		"computer-b": "computer instructions",
	} {
		ctx := workspacepkg.WithWorkspaceTarget(t.Context(), targetID)
		files, err := service.SystemFiles(ctx, workspaceContextTestBotID)
		if err != nil {
			t.Fatalf("SystemFiles(%s) error = %v", targetID, err)
		}
		if len(files) != 1 || files[0].Content != want {
			t.Fatalf("SystemFiles(%s) = %#v, want %q", targetID, files, want)
		}
	}
	if store.getCalls != 2 {
		t.Fatalf("target-scoped database reads = %d, want 2", store.getCalls)
	}
}

func TestStaleCachedGenerationTriggersRefresh(t *testing.T) {
	rawPayload, err := json.Marshal(Payload{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	botUUID := uuid.MustParse(workspaceContextTestBotID)
	store := &cachedSnapshotStore{row: sqlc.BotWorkspaceContextSnapshot{
		BotID:               pgtype.UUID{Bytes: botUUID, Valid: true},
		TargetID:            workspacepkg.WorkspaceTargetNative,
		RequestedGeneration: 2,
		AppliedGeneration:   1,
		Status:              StatusReady,
		Payload:             rawPayload,
		ContentHash:         pgtype.Text{String: "stale", Valid: true},
	}}
	service := NewService(nil, store, nil, nil)

	if _, err := service.GetOrHydrate(t.Context(), workspaceContextTestBotID); err == nil ||
		err.Error() != "unexpected refresh" {
		t.Fatalf("GetOrHydrate() error = %v, want refresh attempt", err)
	}
	if store.getCalls != 1 {
		t.Fatalf("database reads = %d, want 1", store.getCalls)
	}
}

func TestAttachedSnapshotCanAdvanceWithinRequest(t *testing.T) {
	ctx := WithSnapshot(t.Context(), Snapshot{
		BotID:               workspaceContextTestBotID,
		TargetID:            workspacepkg.WorkspaceTargetNative,
		RequestedGeneration: 1,
		AppliedGeneration:   1,
		Status:              StatusReady,
	})

	markContextRefreshing(ctx, workspaceContextTestBotID, workspacepkg.WorkspaceTargetNative, 2)
	refreshing, ok := FromContext(ctx, workspaceContextTestBotID, workspacepkg.WorkspaceTargetNative)
	if !ok || refreshing.Status != StatusRefreshing || refreshing.RequestedGeneration != 2 {
		t.Fatalf("refreshing snapshot = %#v, ok = %v", refreshing, ok)
	}

	updateContextSnapshot(ctx, Snapshot{
		BotID:               workspaceContextTestBotID,
		TargetID:            workspacepkg.WorkspaceTargetNative,
		RequestedGeneration: 2,
		AppliedGeneration:   2,
		Status:              StatusReady,
		ContentHash:         "new",
	})
	ready, ok := FromContext(ctx, workspaceContextTestBotID, workspacepkg.WorkspaceTargetNative)
	if !ok || ready.Status != StatusReady || ready.AppliedGeneration != 2 || ready.ContentHash != "new" {
		t.Fatalf("updated snapshot = %#v, ok = %v", ready, ok)
	}
}

func TestAttachedSourceInvalidSnapshotFailsClosed(t *testing.T) {
	store := &cachedSnapshotStore{}
	service := NewService(nil, store, nil, nil)
	ctx := WithSnapshot(t.Context(), Snapshot{
		BotID:            workspaceContextTestBotID,
		TargetID:         workspacepkg.WorkspaceTargetNative,
		Status:           StatusSourceInvalid,
		LastRefreshError: "invalid hooks",
	})

	if _, err := service.GetOrHydrate(ctx, workspaceContextTestBotID); err == nil {
		t.Fatal("GetOrHydrate() error = nil, want source-invalid failure")
	}
	if store.getCalls != 0 {
		t.Fatalf("database reads = %d, want 0", store.getCalls)
	}
}

func TestAwaitFreshSnapshotDoesNotReturnSupersedingRefreshEarly(t *testing.T) {
	botUUID := uuid.MustParse(workspaceContextTestBotID)
	base := sqlc.BotWorkspaceContextSnapshot{
		BotID:     pgtype.UUID{Bytes: botUUID, Valid: true},
		TargetID:  workspacepkg.WorkspaceTargetNative,
		Status:    StatusRefreshing,
		Payload:   nil,
		TeamID:    pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ID:        pgtype.UUID{Bytes: uuid.New(), Valid: true},
		CreatedAt: pgtype.Timestamptz{},
		UpdatedAt: pgtype.Timestamptz{},
	}
	readyPayload, err := json.Marshal(Payload{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	refreshing := base
	refreshing.RequestedGeneration = 2
	refreshing.AppliedGeneration = 1
	ready := base
	ready.RequestedGeneration = 2
	ready.AppliedGeneration = 2
	ready.Status = StatusReady
	ready.Payload = readyPayload
	ready.ContentHash = pgtype.Text{String: "ready", Valid: true}

	store := &sequenceSnapshotStore{rows: []sqlc.BotWorkspaceContextSnapshot{refreshing, ready}}
	service := NewService(nil, store, nil, nil)
	got, err := service.awaitFreshSnapshot(
		t.Context(),
		pgtype.UUID{Bytes: botUUID, Valid: true},
		workspaceContextTestBotID,
		workspacepkg.WorkspaceTargetNative,
	)
	if err != nil {
		t.Fatalf("awaitFreshSnapshot() error = %v", err)
	}
	if got.Status != StatusReady || got.AppliedGeneration != 2 {
		t.Fatalf("awaitFreshSnapshot() = %#v", got)
	}
	if store.getCalls != 2 {
		t.Fatalf("database reads = %d, want 2", store.getCalls)
	}
}
