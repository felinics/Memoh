package thread

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type subagentConfigQueries struct {
	dbstore.Queries
	inTx          bool
	sessionCreate bool
	configCreate  bool
	contextCreate bool
	contextJSON   []byte
	failConfig    bool
	// parentWorkdir, when set, is returned as the parent session's
	// workdir_id so tests can assert subagent workdir inheritance.
	parentWorkdir pgtype.UUID
}

// GetSessionByID serves the parent-session lookup CreateSubagent performs to
// inherit the parent's workdir binding.
func (q *subagentConfigQueries) GetSessionByID(_ context.Context, id pgtype.UUID) (sqlc.BotSession, error) {
	now := pgtype.Timestamptz{Time: time.Unix(1, 0).UTC(), Valid: true}
	return sqlc.BotSession{
		ID:        id,
		Type:      TypeChat,
		WorkdirID: q.parentWorkdir,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func (q *subagentConfigQueries) InTx(_ context.Context, fn func(dbstore.Queries) error) error {
	q.inTx = true
	err := fn(q)
	if err != nil {
		q.sessionCreate = false
		q.configCreate = false
		q.contextCreate = false
	}
	return err
}

func (q *subagentConfigQueries) CreateSession(_ context.Context, arg sqlc.CreateSessionParams) (sqlc.BotSession, error) {
	q.sessionCreate = true
	now := pgtype.Timestamptz{Time: time.Unix(1, 0).UTC(), Valid: true}
	return sqlc.BotSession{
		ID:              mustSessionUUID("00000000-0000-0000-0000-000000000501"),
		BotID:           arg.BotID,
		Type:            arg.Type,
		SessionMode:     arg.SessionMode,
		RuntimeType:     arg.RuntimeType,
		RuntimeMetadata: arg.RuntimeMetadata,
		Title:           arg.Title,
		Metadata:        arg.Metadata,
		ParentSessionID: arg.ParentSessionID,
		CreatedByUserID: arg.CreatedByUserID,
		WorkdirID:       arg.WorkdirID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

func (q *subagentConfigQueries) CreateSubagentConfig(_ context.Context, arg sqlc.CreateSubagentConfigParams) (sqlc.SubagentConfig, error) {
	if q.failConfig {
		return sqlc.SubagentConfig{}, errors.New("config insert failed")
	}
	q.configCreate = true
	now := pgtype.Timestamptz{Time: time.Unix(1, 0).UTC(), Valid: true}
	return sqlc.SubagentConfig{
		SessionID:    arg.SessionID,
		ModelUuid:    arg.ModelUuid,
		ModelID:      arg.ModelID,
		ProviderName: arg.ProviderName,
		Forked:       arg.Forked,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

func (q *subagentConfigQueries) CreateSubagentForkContext(_ context.Context, arg sqlc.CreateSubagentForkContextParams) (sqlc.CreateSubagentForkContextRow, error) {
	q.contextCreate = true
	q.contextJSON = append(q.contextJSON[:0], arg.ContextMessages...)
	var messages []SubagentForkContextMessage
	if err := json.Unmarshal(arg.ContextMessages, &messages); err != nil {
		return sqlc.CreateSubagentForkContextRow{}, err
	}
	return sqlc.CreateSubagentForkContextRow{InsertedCount: int64(len(messages))}, nil
}

func mustSessionUUID(raw string) pgtype.UUID {
	id, err := dbpkg.ParseUUID(raw)
	if err != nil {
		panic(err)
	}
	return id
}

func TestCreateSubagentPersistsSessionAndConfigInOneTransaction(t *testing.T) {
	queries := &subagentConfigQueries{}
	service := NewService(nil, queries, nil)
	session, config, err := service.CreateSubagent(context.Background(), CreateSubagentInput{
		Thread: CreateInput{
			BotID:           "00000000-0000-0000-0000-000000000502",
			ParentThreadID:  "00000000-0000-0000-0000-000000000503",
			CreatedByUserID: "00000000-0000-0000-0000-000000000504",
			Title:           "worker",
			Metadata:        map[string]any{"agent_id": "worker"},
		},
		ModelUUID:    "00000000-0000-0000-0000-000000000505",
		ModelID:      "worker-model",
		ProviderName: "provider-a",
		Forked:       true,
		ForkContext: []SubagentForkContextMessage{{
			Role:    "user",
			Message: []byte(`{"role":"user","content":[{"type":"text","text":"hello"}]}`),
		}},
	})
	if err != nil {
		t.Fatalf("CreateSubagent: %v", err)
	}
	if !queries.inTx || !queries.sessionCreate || !queries.configCreate || !queries.contextCreate {
		t.Fatalf("expected transactional session+config creation: %+v", queries)
	}
	if session.Type != TypeSubagent || config.ModelID != "worker-model" || config.ProviderName != "provider-a" || !config.Forked {
		t.Fatalf("unexpected created values: session=%+v config=%+v", session, config)
	}
}

func TestCreateSubagentPersistsEmptyForkContextAsJSONArray(t *testing.T) {
	queries := &subagentConfigQueries{}
	svc := NewService(nil, queries, nil)
	_, config, err := svc.CreateSubagent(context.Background(), CreateSubagentInput{
		Thread: CreateInput{
			BotID:          "00000000-0000-0000-0000-000000000501",
			ParentThreadID: "00000000-0000-0000-0000-000000000502",
			Title:          "empty fork",
		},
		ModelUUID:    "00000000-0000-0000-0000-000000000505",
		ModelID:      "worker-model",
		ProviderName: "provider-a",
		Forked:       true,
	})
	if err != nil {
		t.Fatalf("CreateSubagent: %v", err)
	}
	if !queries.contextCreate || string(queries.contextJSON) != "[]" || !config.Forked {
		t.Fatalf("empty fork context was not persisted: queries=%+v config=%+v", queries, config)
	}
}

func TestCreateSubagentRollsBackSessionWhenConfigInsertFails(t *testing.T) {
	queries := &subagentConfigQueries{failConfig: true}
	service := NewService(nil, queries, nil)
	_, _, err := service.CreateSubagent(context.Background(), CreateSubagentInput{
		Thread: CreateInput{
			BotID:          "00000000-0000-0000-0000-000000000502",
			ParentThreadID: "00000000-0000-0000-0000-000000000503",
		},
		ModelUUID:    "00000000-0000-0000-0000-000000000505",
		ModelID:      "worker-model",
		ProviderName: "provider-a",
	})
	if err == nil || !strings.Contains(err.Error(), "config insert failed") {
		t.Fatalf("expected config insert error, got %v", err)
	}
	if queries.sessionCreate || queries.configCreate {
		t.Fatalf("expected transaction rollback, got %+v", queries)
	}
}

func TestSubagentConfigJSONKeepsSessionID(t *testing.T) {
	data, err := json.Marshal(SubagentConfig{ThreadID: "thread-1"})
	if err != nil {
		t.Fatalf("marshal subagent config: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, `"session_id":"thread-1"`) || strings.Contains(got, "thread_id") {
		t.Fatalf("JSON = %s, want only legacy session_id", got)
	}
}

func TestCreateSubagentInheritsParentWorkdir(t *testing.T) {
	parentWorkdir := mustSessionUUID("00000000-0000-0000-0000-000000000601")
	queries := &subagentConfigQueries{parentWorkdir: parentWorkdir}
	svc := NewService(nil, queries, nil)
	session, _, err := svc.CreateSubagent(context.Background(), CreateSubagentInput{
		Thread: CreateInput{
			BotID:          "00000000-0000-0000-0000-000000000502",
			ParentThreadID: "00000000-0000-0000-0000-000000000503",
			Title:          "child",
		},
		ModelUUID:    "00000000-0000-0000-0000-000000000505",
		ModelID:      "worker-model",
		ProviderName: "provider-a",
	})
	if err != nil {
		t.Fatalf("CreateSubagent: %v", err)
	}
	if session.WorkdirID != parentWorkdir.String() {
		t.Fatalf("subagent WorkdirID = %q, want parent's %q", session.WorkdirID, parentWorkdir.String())
	}
}
