package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/testutil/sessionledger"
)

type scriptedReaper struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (r *scriptedReaper) Reap(_ context.Context, _, childID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, childID)
	return r.err
}

type reapTestSessions struct {
	*fakeBackgroundSessionService
	mu        *sync.Mutex
	meta      map[string]any
	child     func() thread.Thread
	deadlines *[]time.Duration
}

func (r reapTestSessions) recordDeadline(ctx context.Context) {
	remaining := time.Duration(-1)
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline)
	}
	*r.deadlines = append(*r.deadlines, remaining)
}

func (r reapTestSessions) ListExternalExecutionHolds(context.Context, string) ([]thread.Thread, error) {
	return []thread.Thread{r.child()}, nil
}

func (r reapTestSessions) ClaimExternalSubagentReap(ctx context.Context, _, runID string, _ time.Duration) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordDeadline(ctx)
	run, unknown := r.meta["external_execution_run"], r.meta["external_termination_unconfirmed"]
	if (run != nil && run != runID) || (unknown != nil && unknown != runID) || (run == nil && unknown == nil) || r.meta["external_termination_reap"] != nil {
		return "", false, nil
	}
	r.meta["external_termination_reap"] = "claim"
	return "claim", true, nil
}

func (r reapTestSessions) ResolveExternalSubagentTermination(ctx context.Context, _, runID, claim string) (thread.Thread, bool, error) {
	r.mu.Lock()
	r.recordDeadline(ctx)
	if r.meta["external_termination_reap"] != claim {
		r.mu.Unlock()
		return thread.Thread{}, false, nil
	}
	delete(r.meta, "external_execution_run")
	delete(r.meta, "external_termination_unconfirmed")
	delete(r.meta, "external_termination_reap")
	r.meta["external_termination_resolved"] = runID
	r.mu.Unlock()
	return r.child(), true, nil
}

type reapTestFixture struct {
	childID    string
	svc        *Service
	reaper     *scriptedReaper
	child      func() thread.Thread
	mu         *sync.Mutex
	meta       map[string]any
	prompts    *[]string
	completed  *bool
	unknownRun string
	deadlines  *[]time.Duration
	messages   *recordingMessageService
}

func newReapTestFixture(t *testing.T) reapTestFixture {
	t.Helper()
	return newReapTestFixtureFor(t, lifecycleTestSessionID)
}

func newReapTestFixtureFor(t *testing.T, childID string) reapTestFixture {
	t.Helper()
	botID, unknownRun := lifecycleTestBotID, uuid.NewString()
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{Ledger: sessionledger.New(), Fence: queueTestFence{}})
	t.Cleanup(func() { _ = manager.CloseContext(context.Background()) })
	var mu sync.Mutex
	meta := map[string]any{"runtime_owner_account_id": "user-1", "project_path": "/data", "external_execution_run": unknownRun, "external_termination_unconfirmed": unknownRun}
	child := func() thread.Thread {
		mu.Lock()
		defer mu.Unlock()
		copied := map[string]any{}
		for key, value := range meta {
			copied[key] = value
		}
		return thread.Thread{ID: childID, BotID: botID, ParentThreadID: "parent", Type: thread.TypeSubagent, SessionMode: thread.TypeSubagent, RuntimeType: "codex", BotAgentID: "backend", CreatedByUserID: "user-1", RuntimeMetadata: copied}
	}
	messages := &recordingMessageService{}
	svc := newACPLifecycleService(t, &recordingACPPrompter{}, messages, &recordingContextLifecycleStore{})
	svc.sessionService = reapTestSessions{fakeBackgroundSessionService: &fakeBackgroundSessionService{getFn: func(_ context.Context, id string) (thread.Thread, error) {
		if id == "parent" {
			return thread.Thread{ID: id, BotID: botID, Type: thread.TypeChat, SessionMode: thread.TypeChat}, nil
		}
		return child(), nil
	}, mergeRuntimeMetadataFn: func(_ context.Context, _, _ string, delta map[string]any) (thread.Thread, error) {
		mu.Lock()
		for key, value := range delta {
			if value == nil {
				delete(meta, key)
			} else {
				meta[key] = value
			}
		}
		mu.Unlock()
		return child(), nil
	}}, mu: &mu, meta: meta, child: child, deadlines: &[]time.Duration{}}
	svc.SetSessionRuntime(manager)
	svc.SetSubagentBackends(&subagentCatalogStub{enabled: true})
	prompts, completed := &[]string{}, new(bool)
	svc.SetExternalRuntimes(noticeTestDriver{kind: "codex", prompt: func(_ context.Context, input external.PromptInput) (external.PromptResult, error) {
		*prompts = append(*prompts, input.Prompt)
		if !*completed {
			return external.PromptResult{}, external.Unavailable(errors.New("launcher missing"))
		}
		return external.PromptResult{TurnCompleted: true, Output: []sdk.Message{sdk.AssistantMessage("checked")}}, nil
	}})
	reaper := &scriptedReaper{err: errors.New("workspace unreachable")}
	svc.SetSubagentProcessReaper(reaper)
	return reapTestFixture{childID: childID, svc: svc, reaper: reaper, child: child, mu: &mu, meta: meta, prompts: prompts, completed: completed, unknownRun: unknownRun, deadlines: svc.sessionService.(reapTestSessions).deadlines, messages: messages}
}

func (f reapTestFixture) run(ctx context.Context, runID, query string) (*tools.SpawnResult, error) {
	parent := tools.SessionContext{BotID: lifecycleTestBotID, SessionID: "parent", UserID: "user-1"}
	handle := sessionruntime.RunHandle{BotID: lifecycleTestBotID, SessionID: f.childID, RunID: runID, FencingToken: 1}
	return f.svc.RunExternalSubagent(withSubagentRunHandle(ctx, handle), tools.ExternalSubagentRun{Parent: parent, SessionID: f.childID, RuntimeType: "codex", BotAgentID: "backend", Query: query, Admission: tools.SubagentAdmission{RunID: handle.RunID, TurnID: handle.RunID}})
}

func TestUnconfirmedSubagentResumesOnlyAfterLeftoversStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	f := newReapTestFixture(t)
	svc, reaper, child, mu, meta, prompts, completed, unknownRun := f.svc, f.reaper, f.child, f.mu, f.meta, f.prompts, f.completed, f.unknownRun
	childID := lifecycleTestSessionID
	parent := tools.SessionContext{BotID: lifecycleTestBotID, SessionID: "parent", UserID: "user-1"}

	for range 2 {
		state, err := svc.SnapshotSubagent(ctx, parent, childID)
		if err != nil || state.Status != "execution_unknown" || state.Code != string(apperror.CodeExternalRuntimeTerminationUnconfirmed) {
			t.Fatalf("unreachable workspace confirmed termination: state=%+v err=%v", state, err)
		}
	}
	if len(reaper.calls) != 1 {
		t.Fatalf("failed confirmation was retried before its claim expired: %d", len(reaper.calls))
	}

	mu.Lock()
	delete(meta, "external_termination_reap")
	mu.Unlock()
	reaper.err = nil
	state, err := svc.SnapshotSubagent(ctx, parent, childID)
	if err != nil || state.Status == "execution_unknown" || state.Code != "" {
		t.Fatalf("confirmed termination still blocks the child: state=%+v err=%v", state, err)
	}
	if got := child().RuntimeMetadata; got["external_execution_run"] != nil || got["external_termination_unconfirmed"] != nil || got["external_termination_resolved"] != unknownRun {
		t.Fatalf("confirmation not recorded: %v", got)
	}
	if err := svc.WaitSubagentIdle(ctx, parent, childID); err != nil {
		t.Fatalf("confirmed child cannot take work: %v", err)
	}

	run := func(runID, query string) (*tools.SpawnResult, error) { return f.run(ctx, runID, query) }
	if _, err := run(uuid.NewString(), "first try"); err == nil {
		t.Fatal("failed dispatch reported success")
	}
	if got := child().RuntimeMetadata; got["external_termination_resolved"] != unknownRun {
		t.Fatalf("a run that never completed used up the notice: %v", got)
	}
	*completed = true
	result, err := run(lifecycleTestRunID, "continue")
	if err != nil || result.Text != "checked" {
		t.Fatalf("confirmed child did not run: result=%+v err=%v", result, err)
	}
	if len(*prompts) != 2 || !strings.HasPrefix((*prompts)[1], subagentStopConfirmedNotice) || !strings.HasSuffix((*prompts)[1], "continue") {
		t.Fatalf("resumed child was not told to check its workspace: %q", *prompts)
	}
	if got := child().RuntimeMetadata; got["external_termination_resolved"] != nil {
		t.Fatalf("notice would repeat after a completed turn: %v", got)
	}
}

func TestSettledSubagentNoLongerReportsItsRunAsUnconfirmed(t *testing.T) {
	ctx := t.Context()
	f := newReapTestFixture(t)
	admitted, err := f.svc.sessionManager.Admit(ctx, sessionruntime.AdmitInput{BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID, InvocationID: "unconfirmed", Payload: []byte(`{}`), Execution: sessionruntime.Execution{Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
		return sessionruntime.RunAdmissionView{}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.sessionManager.FinishRunWithErrorCode(ctx, admitted.Handle, sessionruntime.RunStatusErrored, string(apperror.CodeExternalRuntimeTerminationUnconfirmed)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.meta["external_execution_run"], f.meta["external_termination_unconfirmed"] = admitted.RunID, admitted.RunID
	f.mu.Unlock()
	f.reaper.err = nil
	parent := tools.SessionContext{BotID: lifecycleTestBotID, SessionID: "parent", UserID: "user-1"}
	state, err := f.svc.SnapshotSubagent(ctx, parent, lifecycleTestSessionID)
	if err != nil || state.RunID != admitted.RunID || state.Active || state.Code == string(apperror.CodeExternalRuntimeTerminationUnconfirmed) {
		t.Fatalf("settled child state=%+v err=%v", state, err)
	}
	if f.child().RuntimeMetadata["external_termination_resolved"] != admitted.RunID {
		t.Fatalf("child was not settled: %v", f.child().RuntimeMetadata)
	}
}

func TestDirectSubagentTurnStopsLeftoversBeforeRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	f := newReapTestFixture(t)
	*f.completed = true
	send := func(query string) error {
		events := make(chan WSStreamEvent, 64)
		go func() {
			for range events {
			}
		}()
		defer close(events)
		_, _, err := f.svc.streamChatWSResult(ctx, ChatRequest{BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, RunID: uuid.NewString(), Query: query, UserID: "user-1"}, events, nil)
		return err
	}
	if err := send("blocked"); apperror.CodeOf(err) != apperror.CodeExternalRuntimeTerminationUnconfirmed || len(*f.prompts) != 0 || len(f.reaper.calls) != 1 {
		t.Fatalf("unreachable workspace let the turn run: err=%v prompts=%d reaps=%d", err, len(*f.prompts), len(f.reaper.calls))
	}
	f.mu.Lock()
	delete(f.meta, "external_termination_reap")
	f.mu.Unlock()
	f.reaper.err = nil
	if err := send("continue"); err != nil || len(f.reaper.calls) != 2 {
		t.Fatalf("turn after the workspace recovered stayed blocked: err=%v reaps=%d", err, len(f.reaper.calls))
	}
	if len(*f.prompts) != 1 || !strings.HasPrefix((*f.prompts)[0], subagentStopConfirmedNotice) || !strings.HasSuffix((*f.prompts)[0], "continue") {
		t.Fatalf("turn that settled the stop did not carry the notice: %q", *f.prompts)
	}
	for _, persisted := range f.messages.persisted {
		if strings.Contains(persisted.DisplayText, "[Memoh]") || strings.Contains(string(persisted.Content), "[Memoh]") {
			t.Fatalf("the runtime notice was recorded as the user's message: %+v", persisted)
		}
	}
	if got := f.child().RuntimeMetadata; got["external_termination_unconfirmed"] != nil || got["external_termination_resolved"] != nil {
		t.Fatalf("settled turn left execution records behind: %v", got)
	}
}

func TestSubagentCommandTurnLeavesTheStopNoticeForTheNextPrompt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	f := newReapTestFixture(t)
	*f.completed = true
	f.reaper.err = nil
	send := func(query, command string) error {
		events := make(chan WSStreamEvent, 64)
		go func() {
			for range events {
			}
		}()
		defer close(events)
		_, _, err := f.svc.streamChatWSResult(ctx, ChatRequest{BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, RunID: uuid.NewString(), Query: query, AgentCommand: command, UserID: "user-1"}, events, nil)
		return err
	}
	if err := send("/compact", "compact"); err != nil {
		t.Fatal(err)
	}
	if len(*f.prompts) != 1 || strings.Contains((*f.prompts)[0], subagentStopConfirmedNotice) || f.child().RuntimeMetadata["external_termination_resolved"] != f.unknownRun {
		t.Fatalf("command turn took the notice: prompts=%q meta=%v", *f.prompts, f.child().RuntimeMetadata)
	}
	if err := send("continue", ""); err != nil {
		t.Fatal(err)
	}
	if len(*f.prompts) != 2 || !strings.HasPrefix((*f.prompts)[1], subagentStopConfirmedNotice) || f.child().RuntimeMetadata["external_termination_resolved"] != nil {
		t.Fatalf("next prompt was not warned: prompts=%q meta=%v", *f.prompts, f.child().RuntimeMetadata)
	}
}

func TestSubagentSteerKeepsIngressFaultsPrivate(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	f := newReapTestFixture(t)
	f.mu.Lock()
	delete(f.meta, "external_execution_run")
	delete(f.meta, "external_termination_unconfirmed")
	f.mu.Unlock()
	admission, err := f.svc.sessionManager.Admit(ctx, sessionruntime.AdmitInput{BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID, InvocationID: "child-turn", Payload: []byte(`{"query":"work"}`), Execution: sessionruntime.Execution{Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
		return sessionruntime.RunAdmissionView{}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.sessionManager.EnableSteer(ctx, admission.Handle); err != nil {
		t.Fatal(err)
	}
	parent := tools.SessionContext{BotID: lifecycleTestBotID, SessionID: "parent", UserID: "user-1"}
	_, err = f.svc.ControlSubagent(ctx, tools.SubagentControlInput{Parent: parent, SessionID: lifecycleTestSessionID, RunID: admission.RunID, Action: "steer", Message: "narrow it"})
	if apperror.CodeOf(err) != apperror.CodeQueueAdmissionUnavailable || strings.Contains(err.Error(), "queue:") {
		t.Fatalf("steer ingress fault reached the parent as %v", err)
	}
}

func TestSubagentTerminationRecordsAreBounded(t *testing.T) {
	f := newReapTestFixture(t)
	f.reaper.err = nil
	parent := tools.SessionContext{BotID: lifecycleTestBotID, SessionID: "parent", UserID: "user-1"}
	if state, err := f.svc.SnapshotSubagent(context.Background(), parent, lifecycleTestSessionID); err != nil || state.Status == "execution_unknown" {
		t.Fatalf("termination was not confirmed: state=%+v err=%v", state, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(*f.deadlines) != 2 {
		t.Fatalf("store calls=%v, want claim and resolve", *f.deadlines)
	}
	for _, remaining := range *f.deadlines {
		if remaining <= 0 || remaining > terminalWriteTimeout {
			t.Fatalf("termination record waited without a bound: %v", *f.deadlines)
		}
	}
}

func TestSubagentRunThatDidNotFinishCleanlyStopsWhatItLeft(t *testing.T) {
	for _, tc := range []struct {
		name        string
		result      external.PromptResult
		err         error
		reapErr     error
		wantReaps   int
		wantBlocked bool
	}{
		{"completed", external.PromptResult{TurnCompleted: true, Output: []sdk.Message{sdk.AssistantMessage("done")}}, nil, nil, 0, false},
		{"stopped", external.PromptResult{}, context.Canceled, nil, 1, false},
		{"failed", external.PromptResult{AgentTurnID: "turn"}, errors.New("crashed"), nil, 1, false},
		{"exit unconfirmed", external.PromptResult{TurnCompleted: true, TerminationUnconfirmed: true, Output: []sdk.Message{sdk.AssistantMessage("done")}}, errors.Join(errors.Join(external.ErrTerminationUnconfirmed, errors.New("missing EXIT"))), nil, 1, false},
		{"stopped while unreachable", external.PromptResult{}, context.Canceled, errors.New("workspace unreachable"), 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			f := newReapTestFixture(t)
			f.mu.Lock()
			delete(f.meta, "external_execution_run")
			delete(f.meta, "external_termination_unconfirmed")
			f.mu.Unlock()
			f.reaper.err = tc.reapErr
			f.svc.SetExternalRuntimes(noticeTestDriver{kind: "codex", prompt: func(context.Context, external.PromptInput) (external.PromptResult, error) {
				return tc.result, tc.err
			}})
			runID := uuid.NewString()
			result, err := f.run(ctx, runID, "work")
			meta := f.child().RuntimeMetadata
			if len(f.reaper.calls) != tc.wantReaps {
				t.Fatalf("reaps=%d want %d err=%v", len(f.reaper.calls), tc.wantReaps, err)
			}
			blocked := meta["external_termination_unconfirmed"] == runID && meta["external_execution_run"] == runID
			if blocked != tc.wantBlocked || meta["external_termination_reap"] != nil || (!tc.wantBlocked && (meta["external_execution_run"] != nil || meta["external_termination_unconfirmed"] != nil)) {
				t.Fatalf("blocked=%v want %v meta=%v err=%v", blocked, tc.wantBlocked, meta, err)
			}
			if tc.wantBlocked != (apperror.CodeOf(err) == apperror.CodeExternalRuntimeTerminationUnconfirmed) {
				t.Fatalf("stop reported as %v", err)
			}
			if tc.result.TurnCompleted && (err != nil || result == nil || result.Text != "done") {
				t.Fatalf("completed turn reported as result=%+v err=%v", result, err)
			}
		})
	}
}

func TestReapedSubagentKeepsTheFailureThatEndedItsRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	f := newReapTestFixture(t)
	f.mu.Lock()
	delete(f.meta, "external_execution_run")
	delete(f.meta, "external_termination_unconfirmed")
	f.mu.Unlock()
	f.reaper.err = nil
	exit := errors.Join(external.ErrTerminationUnconfirmed, errors.New("signal: killed"))
	crash := fmt.Errorf("claude CLI exited without a result: %w: tail", exit)
	f.svc.SetExternalRuntimes(noticeTestDriver{kind: "codex", prompt: func(context.Context, external.PromptInput) (external.PromptResult, error) {
		return external.PromptResult{AgentTurnID: "turn", TerminationUnconfirmed: true}, errors.Join(crash, exit)
	}})
	_, err := f.run(ctx, uuid.NewString(), "work")
	if len(f.reaper.calls) != 1 || !errors.Is(apperror.CauseOf(err), crash) || apperror.CodeOf(err) == apperror.CodeExternalRuntimeTerminationUnconfirmed {
		t.Fatalf("reaps=%d err=%v", len(f.reaper.calls), err)
	}
}

func TestSubagentRunNeverReapsAChildItNoLongerOwns(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	f := newReapTestFixture(t)
	f.mu.Lock()
	delete(f.meta, "external_execution_run")
	delete(f.meta, "external_termination_unconfirmed")
	f.mu.Unlock()
	f.reaper.err = nil
	f.svc.SetExternalRuntimes(noticeTestDriver{kind: "codex", prompt: func(context.Context, external.PromptInput) (external.PromptResult, error) {
		f.mu.Lock()
		f.meta["external_execution_run"] = "later-run"
		f.mu.Unlock()
		return external.PromptResult{AgentTurnID: "turn"}, context.Canceled
	}})
	if _, err := f.run(ctx, uuid.NewString(), "work"); err == nil {
		t.Fatal("interrupted run reported success")
	}
	if len(f.reaper.calls) != 0 {
		t.Fatalf("a run that lost the child reaped the later run: %v", f.reaper.calls)
	}
}

func TestSubagentGateReportsFaultsAsFaults(t *testing.T) {
	f := newReapTestFixture(t)
	f.svc.sessionManager = nil
	err := f.svc.externalSubagentExecutionGate(t.Context(), f.child())
	if err == nil || apperror.CodeOf(err) == apperror.CodeExternalRuntimeTerminationUnconfirmed {
		t.Fatalf("a runtime fault was reported as an unconfirmed stop: %v", err)
	}
	f.svc.sessionManager = sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{Ledger: sessionledger.New(), Fence: queueTestFence{}})
	t.Cleanup(func() { _ = f.svc.sessionManager.CloseContext(context.Background()) })
	if err := f.svc.externalSubagentExecutionGate(t.Context(), f.child()); apperror.CodeOf(err) != apperror.CodeExternalRuntimeTerminationUnconfirmed {
		t.Fatalf("an unknown child was not refused: %v", err)
	}
}

func TestSubagentRuntimeControlsRunAsTheChild(t *testing.T) {
	child := thread.Thread{ID: lifecycleTestSessionID, BotID: lifecycleTestBotID, Type: thread.TypeSubagent, SessionMode: thread.TypeSubagent, RuntimeType: "codex", BotAgentID: "backend"}
	input := runtimeControlInput(child, RuntimeControlRequest{BotID: lifecycleTestBotID, ThreadID: child.ID, ActorID: "user-1"})
	if !input.IsSubagent() || !slices.Equal(input.ProcessEnv(), external.SubagentThreadEnvEntry(child.ID)) {
		t.Fatalf("runtime control on a child runs unmarked: mode=%q env=%v", input.SessionMode, input.ProcessEnv())
	}
}
