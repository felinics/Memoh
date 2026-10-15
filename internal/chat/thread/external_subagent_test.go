package thread

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

func TestPostgresCreateExternalSubagentPinsIdentityWithoutNativeConfig(t *testing.T) {
	for _, runtimeType := range []string{RuntimeCodex, RuntimeClaudeCode, RuntimeACPAgent} {
		t.Run(runtimeType, func(t *testing.T) {
			ctx := context.Background()
			tx := beginPostgresSessionTestTx(t, ctx)
			setupPostgresSessionForkFixtures(t, ctx, tx)
			const agentID = "00000000-0000-0000-0000-000000075131"
			const workdirID = "00000000-0000-0000-0000-000000075132"
			agentRuntime := runtimeType
			if runtimeType == RuntimeACPAgent {
				agentRuntime = "acp"
			}
			if _, err := tx.Exec(ctx, `INSERT INTO bot_agents (id, bot_id, name, runtime, metadata)
				VALUES ($1, $2, 'Worker', $3, '{"provider":"custom-agent"}')`, agentID, postgresSessionTestBotID, agentRuntime); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO bot_workdirs (id, bot_id, name, target_kind, path)
				VALUES ($1, $2, 'Parent workdir', 'native', '/data/parent')`, workdirID, postgresSessionTestBotID); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `UPDATE bot_sessions SET workdir_id = $1 WHERE id = $2`, workdirID, postgresSessionTestSessionID); err != nil {
				t.Fatal(err)
			}
			svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)), nil)
			svc.SetACPSetupValidator(testACPSetupValidator{})
			child, config, err := svc.CreateSubagent(ctx, CreateSubagentInput{Thread: CreateInput{
				BotID: postgresSessionTestBotID, BotAgentID: agentID,
				ParentThreadID: postgresSessionTestSessionID, RuntimeType: runtimeType,
				Metadata:        map[string]any{"agent_id": "worker", "project_path": "/data/wrong"},
				RuntimeMetadata: map[string]any{"runtime_owner_account_id": postgresSessionTestUserID, "acp_agent_id": "custom-agent"},
			}})
			if err != nil {
				t.Fatalf("CreateSubagent: %v", err)
			}
			if config != (SubagentConfig{}) {
				t.Fatalf("external child has native configuration: %+v", config)
			}
			if child.Type != TypeSubagent || child.SessionMode != TypeSubagent || child.RuntimeType != runtimeType || child.BotAgentID != agentID || child.ParentThreadID != postgresSessionTestSessionID || child.WorkdirID != workdirID || child.Visibility != VisibilityInternal {
				t.Fatalf("child identity = %+v", child)
			}
			if child.RuntimeMetadata["runtime_owner_account_id"] != postgresSessionTestUserID || child.RuntimeMetadata["project_path"] != "/data/parent" || child.Metadata["project_path"] != "/data/parent" {
				t.Fatalf("child runtime metadata = %+v, metadata = %+v", child.RuntimeMetadata, child.Metadata)
			}
			if _, err := svc.GetSubagentConfig(ctx, child.ID); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("GetSubagentConfig = %v, want no native config", err)
			}
			listed, err := svc.ListSubagentsByParent(ctx, postgresSessionTestSessionID)
			if err != nil || len(listed) != 1 || listed[0].ID != child.ID {
				t.Fatalf("ListSubagentsByParent = %+v, %v", listed, err)
			}
			for _, update := range []struct {
				name, mode, runtime, binding string
			}{
				{"native fallback", TypeSubagent, RuntimeModel, agentID},
				{"mode change", TypeChat, runtimeType, agentID},
				{"binding removed", TypeSubagent, runtimeType, ""},
			} {
				_, err := svc.UpdateDescriptorAndMetadataWithOwner(ctx, child.ID, TypeSubagent, update.mode, update.runtime, child.Metadata, child.RuntimeMetadata, &update.binding, postgresSessionTestUserID)
				if err == nil {
					t.Fatalf("%s unexpectedly succeeded", update.name)
				}
			}
			changedRuntime := map[string]any{"runtime_owner_account_id": "00000000-0000-0000-0000-000000075199", "project_path": "/data/rebound", "acp_agent_id": "other-agent"}
			if _, err := svc.MergeRuntimeMetadata(ctx, child.ID, runtimeType, map[string]any{"external_termination_unconfirmed": "old-run", "external_execution_run": "old-run", "external_termination_reap": "claim", "external_termination_resolved": "older-run"}); err != nil {
				t.Fatal(err)
			}
			updated, err := svc.UpdateDescriptorAndMetadataWithOwner(ctx, child.ID, TypeSubagent, TypeSubagent, runtimeType, child.Metadata, changedRuntime, nil, "00000000-0000-0000-0000-000000075199")
			if err != nil || updated.RuntimeMetadata["runtime_owner_account_id"] != postgresSessionTestUserID || updated.RuntimeMetadata["project_path"] != "/data/parent" {
				t.Fatalf("runtime metadata changed pinned owner or workdir: %+v, %v", updated, err)
			}
			if updated.RuntimeMetadata["external_termination_unconfirmed"] != "old-run" || updated.RuntimeMetadata["external_execution_run"] != "old-run" || updated.RuntimeMetadata["external_termination_reap"] != "claim" || updated.RuntimeMetadata["external_termination_resolved"] != "older-run" {
				t.Fatal("ordinary metadata update cleared unknown execution gate")
			}
			if _, err := tx.Exec(ctx, `DELETE FROM bot_agents WHERE id = $1`, agentID); err != nil {
				t.Fatal(err)
			}
			listed, err = svc.ListSubagentsByParent(ctx, postgresSessionTestSessionID)
			if err != nil || len(listed) != 1 || listed[0].ID != child.ID || listed[0].RuntimeType != runtimeType || listed[0].BotAgentID != "" {
				t.Fatalf("deleted binding hid or changed child: %+v, %v", listed, err)
			}
		})
	}
}

func TestPostgresCreateExternalSubagentRejectsInvalidBindingsWithoutRows(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresSessionTestTx(t, ctx)
	setupPostgresSessionForkFixtures(t, ctx, tx)
	const agentID = "00000000-0000-0000-0000-000000075131"
	if _, err := tx.Exec(ctx, `INSERT INTO bot_agents (id, bot_id, name, runtime) VALUES ($1, $2, 'Worker', 'codex')`, agentID, postgresSessionTestBotID); err != nil {
		t.Fatal(err)
	}
	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)), nil)
	for _, tc := range []struct {
		name   string
		change func(*CreateSubagentInput)
	}{
		{"missing parent", func(input *CreateSubagentInput) { input.Thread.ParentThreadID = "" }},
		{"missing binding", func(input *CreateSubagentInput) { input.Thread.BotAgentID = "" }},
		{"wrong runtime", func(input *CreateSubagentInput) { input.Thread.RuntimeType = RuntimeClaudeCode }},
		{"missing owner", func(input *CreateSubagentInput) { delete(input.Thread.RuntimeMetadata, "runtime_owner_account_id") }},
		{"invalid owner", func(input *CreateSubagentInput) {
			input.Thread.RuntimeMetadata["runtime_owner_account_id"] = "channel-identity"
		}},
		{"different workdir", func(input *CreateSubagentInput) { input.Thread.WorkdirID = "00000000-0000-0000-0000-000000075199" }},
		{"native model", func(input *CreateSubagentInput) { input.ModelID = "native-model" }},
		{"native fork", func(input *CreateSubagentInput) { input.Forked = true }},
		{"another bot", func(input *CreateSubagentInput) { input.Thread.BotID = "00000000-0000-0000-0000-000000075199" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := CreateSubagentInput{Thread: CreateInput{
				BotID: postgresSessionTestBotID, BotAgentID: agentID,
				RuntimeType: RuntimeCodex, ParentThreadID: postgresSessionTestSessionID,
				RuntimeMetadata: map[string]any{"runtime_owner_account_id": postgresSessionTestUserID},
			}}
			tc.change(&input)
			if _, _, err := svc.CreateSubagent(ctx, input); !errors.Is(err, ErrInvalidExternalSubagent) {
				t.Fatalf("CreateSubagent = %v, want invalid binding", err)
			}
			if rows := countPostgresSessionTestRows(t, ctx, tx, "bot_sessions"); rows != 1 {
				t.Fatalf("invalid creation left %d sessions, want original parent only", rows)
			}
		})
	}
}
