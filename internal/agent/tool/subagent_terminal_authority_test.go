package tools

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/background"
)

type terminalRaceSpawnAgent struct {
	terminalResult *SpawnResult
	terminalErr    error
	resolved       chan struct{}
	release        chan struct{}
}

func TestRunSubagentTaskRejectsCleanEndAfterOwningAbort(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	agent := &mockSpawnAgent{
		generateFunc: func(_ context.Context, cfg SpawnRunConfig, _ func()) (*SpawnResult, error) {
			cancel(context.Canceled)
			if cfg.ResolveCompletion == nil {
				t.Fatal("completion resolver is nil")
			}
			if got := cfg.ResolveCompletion(); got != SpawnAttemptAbort {
				t.Fatalf("completion disposition = %v, want abort", got)
			}
			return &SpawnResult{Text: "late success"}, nil
		},
	}
	p := &SpawnProvider{
		agent:  agent,
		logger: slog.New(slog.DiscardHandler),
		modelResolver: func(context.Context, SessionContext, string, string, string) (resolvedSubagentModel, error) {
			return resolvedSubagentModel{}, nil
		},
	}

	result := p.runSubagentTask(ctx, &agentRequest{
		taskID:         "task-clean-end-abort",
		agentID:        "worker",
		agentSessionID: "session-clean-end-abort",
		message:        "work",
		parentSession:  SessionContext{BotID: "bot-1"},
	})

	if !result.AttemptResolved || result.AttemptOutcome != SpawnAttemptAbort {
		t.Fatalf("attempt outcome = resolved %v disposition %v, want abort", result.AttemptResolved, result.AttemptOutcome)
	}
	if !errors.Is(result.Cause, context.Canceled) || result.Text != "" {
		t.Fatalf("terminal result = %+v, want canceled without late success", result)
	}
}

func (a *terminalRaceSpawnAgent) Generate(ctx context.Context, cfg SpawnRunConfig) (*SpawnResult, error) {
	return a.GenerateWithWatchdog(ctx, cfg, func() {})
}

func (a *terminalRaceSpawnAgent) GenerateWithWatchdog(_ context.Context, cfg SpawnRunConfig, _ func()) (*SpawnResult, error) {
	if a.terminalErr != nil {
		if cfg.ResolveAttempt == nil {
			return nil, errors.New("attempt resolver is unavailable")
		}
		if got := cfg.ResolveAttempt(a.terminalErr); got != SpawnAttemptFailure {
			return nil, errors.New("attempt resolver did not preserve failure")
		}
	} else {
		if cfg.ResolveCompletion == nil {
			return nil, errors.New("completion resolver is unavailable")
		}
		if got := cfg.ResolveCompletion(); got != SpawnAttemptCompleted {
			return nil, errors.New("completion resolver did not preserve completion")
		}
	}
	close(a.resolved)
	<-a.release
	return a.terminalResult, a.terminalErr
}

func TestSpawnedResolvedTerminalSurvivesLateBackgroundStop(t *testing.T) {
	tests := []struct {
		name       string
		result     *SpawnResult
		err        error
		wantStatus background.TaskStatus
		wantReport string
		wantError  string
	}{
		{
			name:       "completed",
			result:     &SpawnResult{Text: "finished report"},
			wantStatus: background.TaskCompleted,
			wantReport: "finished report",
		},
		{
			name:       "failed",
			err:        errors.New("provider failed before cancellation"),
			wantStatus: background.TaskFailed,
			wantError:  "provider failed before cancellation",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agent := &terminalRaceSpawnAgent{
				terminalResult: tc.result,
				terminalErr:    tc.err,
				resolved:       make(chan struct{}),
				release:        make(chan struct{}),
			}
			mgr := background.New(nil)
			p := NewSpawnProvider(nil, nil, nil, nil, nil, mgr)
			p.sessionService = &fakeAgentSessionService{}
			p.SetAgent(agent)
			p.SetMessageService(newFakeAgentMessageService())
			p.SetSubagentAdmitter(&fakeSubagentAdmitter{})
			p.modelResolver = func(context.Context, SessionContext, string, string, string) (resolvedSubagentModel, error) {
				return resolvedSubagentModel{
					Model:            &sdk.Model{},
					UUID:             "00000000-0000-0000-0000-000000000123",
					ModelID:          "test-model",
					ProviderName:     "test-provider",
					SupportsToolCall: true,
				}, nil
			}

			toolList, err := p.Tools(context.Background(), SessionContext{BotID: "bot1", SessionID: "parent1"})
			if err != nil {
				t.Fatalf("Tools returned error: %v", err)
			}
			var spawnTool sdk.Tool
			for _, tool := range toolList {
				if tool.Name == "spawn_agent" {
					spawnTool = tool
					break
				}
			}
			if spawnTool.Name == "" {
				t.Fatal("spawn_agent tool is unavailable")
			}

			type execution struct {
				result any
				err    error
			}
			done := make(chan execution, 1)
			go func() {
				result, err := spawnTool.Execute(&sdk.ToolExecContext{Context: context.Background()}, map[string]any{
					"id":   "worker",
					"task": "finish once",
				})
				done <- execution{result: result, err: err}
			}()

			select {
			case <-agent.resolved:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for terminal resolution")
			}
			snapshots := mgr.ListSnapshotsForSession("bot1", "parent1")
			if len(snapshots) != 1 {
				t.Fatalf("background snapshots = %#v, want one agent task", snapshots)
			}
			if err := mgr.Kill(snapshots[0].TaskID); err != nil {
				t.Fatalf("late Kill returned error: %v", err)
			}
			if snapshot := mgr.Get(snapshots[0].TaskID).Snapshot(); snapshot.Status != background.TaskRunning {
				t.Fatalf("late stop published premature terminal snapshot: %+v", snapshot)
			}
			close(agent.release)

			select {
			case execution := <-done:
				if execution.err != nil {
					t.Fatalf("spawn_agent returned error: %v", execution.err)
				}
				result, ok := execution.result.(map[string]any)
				if !ok || result["status"] != string(tc.wantStatus) {
					t.Fatalf("result = %#v, want status %s", execution.result, tc.wantStatus)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for spawned task completion")
			}

			snapshot := mgr.Get(snapshots[0].TaskID).Snapshot()
			if snapshot.Status != tc.wantStatus || snapshot.AgentReport != tc.wantReport || snapshot.AgentError != tc.wantError {
				t.Fatalf("background snapshot = %+v, want status %s report %q error %q", snapshot, tc.wantStatus, tc.wantReport, tc.wantError)
			}
		})
	}
}
