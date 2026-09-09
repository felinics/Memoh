package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	acpagent "github.com/felinics/memoh/internal/agent/runtime/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/contextview"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/schedule"
)

type triggerLifecycleProvider struct {
	mu     sync.Mutex
	calls  int
	params sdk.GenerateParams
}

func (*triggerLifecycleProvider) Name() string { return "trigger-lifecycle" }

func (*triggerLifecycleProvider) ListModels(context.Context) ([]sdk.Model, error) { return nil, nil }

func (*triggerLifecycleProvider) Test(context.Context) *sdk.ProviderTestResult {
	return &sdk.ProviderTestResult{Status: sdk.ProviderStatusOK}
}

func (*triggerLifecycleProvider) TestModel(context.Context, string) (*sdk.ModelTestResult, error) {
	return &sdk.ModelTestResult{Supported: true}, nil
}

func (p *triggerLifecycleProvider) DoGenerate(
	_ context.Context,
	params sdk.GenerateParams,
) (*sdk.GenerateResult, error) {
	p.mu.Lock()
	p.calls++
	p.params = params
	p.mu.Unlock()
	return &sdk.GenerateResult{
		Text:         directLifecycleResponse,
		FinishReason: sdk.FinishReasonStop,
		Messages: []sdk.Message{
			sdk.AssistantMessage(directLifecycleResponse),
		},
	}, nil
}

func (p *triggerLifecycleProvider) DoStream(
	ctx context.Context,
	params sdk.GenerateParams,
) (*sdk.StreamResult, error) {
	result, err := p.DoGenerate(ctx, params)
	if err != nil {
		return nil, err
	}
	ch := make(chan sdk.StreamPart, 8)
	ch <- &sdk.StartPart{}
	ch <- &sdk.StartStepPart{}
	ch <- &sdk.TextStartPart{ID: "answer"}
	ch <- &sdk.TextDeltaPart{ID: "answer", Text: result.Text}
	ch <- &sdk.FinishStepPart{FinishReason: result.FinishReason}
	ch <- &sdk.FinishPart{FinishReason: result.FinishReason}
	close(ch)
	return &sdk.StreamResult{Stream: ch}, nil
}

func (p *triggerLifecycleProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func configureTriggerLifecycleContextView(
	t *testing.T,
	fixture directLifecycleFixture,
	mutate func(*agentpkg.RunConfig),
) *triggerLifecycleProvider {
	t.Helper()
	provider := &triggerLifecycleProvider{}
	logger := slog.New(slog.DiscardHandler)
	applier := contextview.ProviderRunConfigApplier(logger)
	fixture.service.agent = agentpkg.New(agentpkg.Deps{
		Logger: logger,
		ContextViewApplier: func(ctx context.Context, cfg agentpkg.RunConfig) (agentpkg.RunConfig, error) {
			if mutate != nil {
				mutate(&cfg)
			}
			out, err := applier(ctx, cfg)
			if out.Model != nil {
				model := *out.Model
				model.Provider = provider
				out.Model = &model
			}
			return out, err
		},
	})
	return provider
}

func setDirectLifecycleContextWindow(t *testing.T, fixture directLifecycleFixture, window int) {
	t.Helper()
	queries, ok := fixture.service.queries.(*directLifecycleQueries)
	if !ok {
		t.Fatalf("direct lifecycle queries = %T, want *directLifecycleQueries", fixture.service.queries)
	}
	for key, model := range queries.models {
		model.Config = []byte(`{"context_window":` + strconv.Itoa(window) + `}`)
		queries.models[key] = model
	}
}

func assertDeferredLifecycleRow(
	t *testing.T,
	creates []sqlc.CreateContextLifecycleParams,
	runID, status, errorCode string,
) contextfrag.LifecycleSnapshot {
	t.Helper()
	if len(creates) != 1 {
		t.Fatalf("CreateContextLifecycle calls = %d, want 1", len(creates))
	}
	row := creates[0]
	if got := pgUUIDString(row.RunID); got != runID {
		t.Fatalf("context lifecycle run ID = %q, want admitted run ID %q", got, runID)
	}
	if row.Status != status {
		t.Fatalf("context lifecycle status = %q, want %q", row.Status, status)
	}
	if row.ErrorCode.String != errorCode || row.ErrorCode.Valid != (errorCode != "") {
		t.Fatalf("context lifecycle error code = %#v, want %q", row.ErrorCode, errorCode)
	}
	for _, privateText := range []string{directLifecyclePrompt, directLifecycleResponse} {
		if bytes.Contains(row.Snapshot, []byte(privateText)) {
			t.Fatalf("content-light snapshot leaked %q: %s", privateText, row.Snapshot)
		}
	}
	var snapshot contextfrag.LifecycleSnapshot
	if err := json.Unmarshal(row.Snapshot, &snapshot); err != nil {
		t.Fatalf("decode lifecycle snapshot: %v", err)
	}
	if snapshot.Version != contextfrag.LifecycleSnapshotVersion {
		t.Fatalf("snapshot version = %d, want 1", snapshot.Version)
	}
	return snapshot
}

func hasLifecycleMutation(snapshot contextfrag.LifecycleSnapshot, kind contextfrag.MutationKind) bool {
	for _, mutation := range snapshot.Mutations {
		if mutation.Kind == kind {
			return true
		}
	}
	return false
}

func triggerDirectSchedule(t *testing.T, service *Service) (schedule.TriggerResult, error) {
	t.Helper()
	return service.TriggerSchedule(context.Background(), lifecycleTestBotID, schedule.TriggerPayload{
		SessionID:   lifecycleTestSessionID,
		Command:     directLifecyclePrompt,
		OwnerUserID: "user-1",
	}, "")
}

func TestTriggerScheduleResolveFailurePersistsAdmittedMinimalLifecycle(t *testing.T) {
	fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)
	fixture.service.settingsService = nil

	_, err := triggerDirectSchedule(t, fixture.service)
	if err == nil {
		t.Fatal("TriggerSchedule() error = nil, want model-resolution failure")
	}
	creates := fixture.lifecycles.creates()
	if len(creates) != 1 {
		t.Fatalf("lifecycle creates = %d, want 1", len(creates))
	}
	row := creates[0]
	if pgUUIDString(row.RunID) != lifecycleTestRunID || row.Status != contextLifecycleStatusFailedProvider {
		t.Fatalf("lifecycle terminal = (run %q, status %q), want admitted run %q failed_provider", pgUUIDString(row.RunID), row.Status, lifecycleTestRunID)
	}
	var snapshot contextfrag.LifecycleSnapshot
	if err := json.Unmarshal(row.Snapshot, &snapshot); err != nil {
		t.Fatalf("decode minimal lifecycle: %v", err)
	}
	if snapshot.Version != contextfrag.LifecycleSnapshotVersion || snapshot.View != "" || snapshot.Counts != (contextfrag.ManifestCounts{}) ||
		snapshot.AssistantMessageID != "" {
		t.Fatalf("pre-context lifecycle is not minimal: %#v", snapshot)
	}
	if len(fixture.messages.persisted) != 0 {
		t.Fatalf("resolve failure persisted messages: %#v", fixture.messages.persisted)
	}
	if len(fixture.runtime.finishes) != 1 || fixture.runtime.finishes[0].status != sessionruntime.RunStatusErrored {
		t.Fatalf("runtime finishes = %#v, want one errored finish", fixture.runtime.finishes)
	}
}

func TestTriggerScheduleProviderFailurePersistsFailedProviderLifecycle(t *testing.T) {
	fixture := newDirectLifecycleFixture(t, directLifecycleModelFailure)

	_, err := triggerDirectSchedule(t, fixture.service)
	if err == nil {
		t.Fatal("TriggerSchedule() error = nil, want provider failure")
	}
	assertDirectLifecycle(t, fixture.lifecycles, lifecycleTestRunID, contextLifecycleStatusFailedProvider, "")
	if len(fixture.runtime.finishes) != 1 || fixture.runtime.finishes[0].status != sessionruntime.RunStatusErrored {
		t.Fatalf("runtime finishes = %#v, want one errored finish", fixture.runtime.finishes)
	}
	if fixture.lifecycles.creates()[0].ErrorCode.Valid {
		t.Fatalf("private provider diagnostic became stable error code: %#v", fixture.lifecycles.creates()[0].ErrorCode)
	}
}

func TestTriggerScheduleProviderBudgetFailurePersistsFailedBudgetWithoutAssistant(t *testing.T) {
	fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)
	setDirectLifecycleContextWindow(t, fixture, 1)
	provider := configureTriggerLifecycleContextView(t, fixture, nil)

	_, err := triggerDirectSchedule(t, fixture.service)
	if apperror.CodeOf(err) != apperror.CodeContextBudgetUnsatisfied {
		t.Fatalf("TriggerSchedule() error = %v, want code %v", err, apperror.CodeContextBudgetUnsatisfied)
	}
	if provider.callCount() != 0 {
		t.Fatalf("provider calls = %d, want 0 after provider-budget rejection", provider.callCount())
	}
	if len(fixture.messages.persisted) != 0 {
		t.Fatalf("persisted messages = %#v, want none after provider-budget rejection", fixture.messages.persisted)
	}
	assertDirectLifecycle(
		t,
		fixture.lifecycles,
		lifecycleTestRunID,
		contextLifecycleStatusFailedBudget,
		"",
	)
	snapshot := assertDeferredLifecycleRow(
		t,
		fixture.lifecycles.creates(),
		lifecycleTestRunID,
		contextLifecycleStatusFailedBudget,
		string(apperror.CodeContextBudgetUnsatisfied),
	)
	if snapshot.BudgetPlan == nil || snapshot.BudgetPlan.Window != 1 {
		t.Fatalf("budget plan = %#v, want active provider plan with window 1", snapshot.BudgetPlan)
	}
	if !hasLifecycleMutation(snapshot, contextfrag.MutationContextBudgetFailure) {
		t.Fatalf("lifecycle mutations = %#v, want provider-budget failure", snapshot.Mutations)
	}
	if len(fixture.runtime.finishes) != 1 || fixture.runtime.finishes[0].status != sessionruntime.RunStatusErrored {
		t.Fatalf("runtime finishes = %#v, want one errored finish", fixture.runtime.finishes)
	}
}

func TestTriggerScheduleContextViewFallbackPersistsFallbackAndReachesProvider(t *testing.T) {
	fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)
	provider := configureTriggerLifecycleContextView(t, fixture, func(cfg *agentpkg.RunConfig) {
		duplicate := func(role sdk.MessageRole, text string) contextfrag.ContextFrag {
			return contextfrag.MessageFrag(contextfrag.MessageFragInput{
				ID:      "forced-duplicate",
				Message: sdk.Message{Role: role, Content: []sdk.MessagePart{sdk.TextPart{Text: text}}},
				Kind:    contextfrag.KindConversationEvent,
				Slot:    contextfrag.SlotHistory,
				Source:  contextfrag.SourceRunConfig,
				Scope:   cfg.ContextScope,
			})
		}
		cfg.ContextSourceFrags = append(
			cfg.ContextSourceFrags,
			duplicate(sdk.MessageRoleUser, "first"),
			duplicate(sdk.MessageRoleAssistant, "second"),
		)
	})

	result, err := triggerDirectSchedule(t, fixture.service)
	if err != nil {
		t.Fatalf("TriggerSchedule() error = %v", err)
	}
	if result.Status != "ok" {
		t.Fatalf("TriggerSchedule() status = %q, want ok", result.Status)
	}
	if provider.callCount() != 1 {
		t.Fatalf("provider calls = %d, want fallback payload to reach provider once", provider.callCount())
	}
	assertDirectLifecycle(
		t,
		fixture.lifecycles,
		lifecycleTestRunID,
		contextLifecycleStatusFallback,
		"message-id",
	)
	snapshot := assertDeferredLifecycleRow(
		t,
		fixture.lifecycles.creates(),
		lifecycleTestRunID,
		contextLifecycleStatusFallback,
		"",
	)
	if !hasLifecycleMutation(snapshot, contextfrag.MutationContextViewFallback) {
		t.Fatalf("lifecycle mutations = %#v, want context-view fallback", snapshot.Mutations)
	}
}

func TestTriggerScheduleLifecycleStoreFailureDoesNotFailSuccessfulTrigger(t *testing.T) {
	fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)
	fixture.lifecycles.mu.Lock()
	fixture.lifecycles.store.createErr = errors.New("context lifecycle store unavailable")
	fixture.lifecycles.mu.Unlock()

	result, err := triggerDirectSchedule(t, fixture.service)
	if err != nil {
		t.Fatalf("TriggerSchedule() error = %v, want nil despite lifecycle store failure", err)
	}
	if result.Status != "ok" {
		t.Fatalf("TriggerSchedule() status = %q, want ok", result.Status)
	}
	if got := fixture.service.contextLifecyclePersistenceErrors.Load(); got == 0 {
		t.Fatal("lifecycle persistence error count = 0, want at least one recorded failure")
	}
	if len(fixture.runtime.finishes) != 1 || fixture.runtime.finishes[0].status != sessionruntime.RunStatusCompleted {
		t.Fatalf("runtime finishes = %#v, want one completed finish", fixture.runtime.finishes)
	}
}

func TestTriggerScheduleACPPersistsCompletedLifecycle(t *testing.T) {
	pool := &recordingACPPrompter{result: acpclient.PromptResult{Text: "done", StopReason: "end_turn"}}
	messages := &recordingMessageService{}
	lifecycles := &recordingContextLifecycleStore{}
	service := newACPLifecycleService(t, pool, messages, lifecycles)

	result, err := service.triggerScheduleRuntime(
		context.Background(),
		lifecycleTestBotID,
		schedule.TriggerPayload{
			SessionID:       lifecycleTestSessionID,
			Command:         "run scheduled task",
			OwnerUserID:     "user-1",
			ACPModelID:      "test-model",
			ReasoningEffort: "medium",
		},
		"",
		lifecycleTestRunID,
		acpagent.NewDriver(pool),
	)
	if err != nil {
		t.Fatalf("triggerScheduleRuntime() error = %v", err)
	}
	if result.Status != "ok" || result.Text != "done" {
		t.Fatalf("triggerScheduleRuntime() result = %#v, want completed output", result)
	}
	if pool.input.RunID != lifecycleTestRunID || pool.input.SessionID != lifecycleTestSessionID {
		t.Fatalf("ACP prompt identity = (run %q, session %q), want (%q, %q)", pool.input.RunID, pool.input.SessionID, lifecycleTestRunID, lifecycleTestSessionID)
	}
	row, snapshot := requireACPLifecycle(t, lifecycles, lifecycleTestRunID, contextLifecycleStatusCompleted)
	if row.ErrorCode.Valid {
		t.Fatalf("completed lifecycle error code = %#v, want none", row.ErrorCode)
	}
	if snapshot.AssistantMessageID != "message-id" {
		t.Fatalf("assistant message ID = %q, want message-id", snapshot.AssistantMessageID)
	}
}

type incompleteScheduleDriver struct {
	input external.PromptInput
}

func (*incompleteScheduleDriver) RuntimeType() string { return "codex" }

func (d *incompleteScheduleDriver) Prompt(_ context.Context, input external.PromptInput) (external.PromptResult, error) {
	d.input = input
	return external.PromptResult{Text: "partial", Output: []sdk.Message{sdk.AssistantMessage("partial")}}, nil
}

func TestTriggerScheduleRuntimeRejectsIncompleteTurn(t *testing.T) {
	driver := &incompleteScheduleDriver{}
	messages := &recordingMessageService{}
	lifecycles := &recordingContextLifecycleStore{}
	service := newACPLifecycleService(t, &recordingACPPrompter{}, messages, lifecycles)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := service.triggerScheduleRuntime(
		ctx,
		lifecycleTestBotID,
		schedule.TriggerPayload{SessionID: lifecycleTestSessionID, Command: "run scheduled task", OwnerUserID: "user-1"},
		"",
		lifecycleTestRunID,
		driver,
	)
	if err == nil {
		t.Fatal("triggerScheduleRuntime() error = nil, want incomplete-turn failure")
	}
	if driver.input.CanRequestUserInput {
		t.Fatal("scheduled external runtime was marked interactive")
	}
	_, snapshot := requireACPLifecycle(t, lifecycles, lifecycleTestRunID, contextLifecycleStatusAborted)
	if snapshot.AssistantMessageID != "message-id" {
		t.Fatalf("aborted lifecycle assistant message ID = %q, want message-id", snapshot.AssistantMessageID)
	}
}
