package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/contextview"
)

type discussBudgetCompactor struct {
	configs []compaction.TriggerConfig
	status  string
	err     error
	delay   time.Duration
	started chan struct{}
}

func (c *discussBudgetCompactor) RunCompactionSync(ctx context.Context, cfg compaction.TriggerConfig) (compaction.Result, error) {
	c.configs = append(c.configs, cfg)
	if c.started != nil {
		close(c.started)
	}
	if c.delay > 0 {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return compaction.Result{}, context.Cause(ctx)
		}
	}
	return compaction.Result{Status: c.status}, c.err
}

func TestDiscussProviderBudgetRecoveryDoesNotConsumeModelIdleTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, _, provider, compactor, cmd := discussBudgetRecoveryFixture(t)
		configureDiscussLifecycle(service)
		service.streamIdleTimeout = 10 * time.Millisecond
		compactor.delay = 50 * time.Millisecond
		handle, err := service.StartTurn(context.Background(), cmd)
		if err != nil {
			t.Fatal(err)
		}
		recomposed := false
		for event := range handle.Events() {
			recomposed = recomposed || event.Kind == turn.DiscussEventRecompose
		}
		for err := range handle.Errs() {
			t.Errorf("active compaction timed out as model silence: %v", err)
		}
		if !recomposed || len(compactor.configs) != 1 || provider.callCount() != 0 {
			t.Fatalf("recompose=%t compactor/provider=%d/%d, want recompose after one compaction and no provider call", recomposed, len(compactor.configs), provider.callCount())
		}
	})
}

func TestDiscussProviderBudgetRecoveryStillHonorsCancellation(t *testing.T) {
	service, _, provider, compactor, cmd := discussBudgetRecoveryFixture(t)
	runtime, _ := configureDiscussLifecycle(service)
	compactor.delay = time.Hour
	compactor.started = make(chan struct{})
	handle, err := service.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-compactor.started:
	case <-time.After(time.Second):
		t.Fatal("compaction did not start")
	}
	handle.Cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range handle.Events() {
		}
		for range handle.Errs() {
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not interrupt budget recovery")
	}
	if provider.callCount() != 0 || len(runtime.finishes) != 1 || runtime.finishes[0].status != sessionruntime.RunStatusAborted {
		t.Fatalf("provider=%d finishes=%#v, want canceled compaction without provider dispatch", provider.callCount(), runtime.finishes)
	}
}

func discussBudgetRecoveryFixture(t *testing.T) (*Service, *fakeDiscussService, *countingDiscussLifecycleProvider, *discussBudgetCompactor, turn.StartTurnCommand) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	provider := &countingDiscussLifecycleProvider{}
	resolver := &fakeDiscussService{resolveResult: ResolveRunConfigResult{
		RunConfig: native.RunConfig{
			Model:           &sdk.Model{ID: "discuss-budget", Provider: provider},
			System:          strings.Repeat("s", 3200),
			ContextToolDefs: []contextfrag.ToolDefAccounting{{Name: "large_roster", TokenEstimate: 10000}},
		},
		ContextBudgetMaxTokens: 16000,
	}}
	agent := native.New(native.Deps{Logger: logger, ContextViewApplier: contextview.ProviderRunConfigApplier(logger)})
	service := newDiscussTestService(&fakeRunner{}, agent, resolver)
	policyService, _ := newControllerPolicyService(t, nil)
	service.settingsService = policyService.settingsService
	service.modelsService = policyService.modelsService
	service.queries = policyService.queries
	compactor := &discussBudgetCompactor{status: compaction.StatusOK}
	service.compactionService = compactor
	cmd := lifecycleDiscussCommand()
	cmd.DiscussMessages = nil
	for i := 0; i < 20; i++ {
		cmd.DiscussMessages = append(cmd.DiscussMessages, turn.DiscussMessage{
			Role: "user", Content: strings.Repeat("s", 200), CompactionArtifactID: fmt.Sprintf("summary-%d", i),
		})
	}
	cmd.DiscussMessages = append(cmd.DiscussMessages,
		turn.DiscussMessage{Role: "assistant", Content: strings.Repeat("h", 8000)},
		turn.DiscussMessage{Role: "user", Content: "continue"},
	)
	return service, resolver, provider, compactor, cmd
}

func TestDiscussProviderBudgetRecoveryRecomposesBeforeProviderDispatch(t *testing.T) {
	service, resolver, provider, compactor, cmd := discussBudgetRecoveryFixture(t)
	runtime, lifecycles := configureDiscussLifecycle(service)
	var published []native.StreamEventType
	service.publishTurnEvent = func(_ context.Context, _ sessionruntime.RunHandle, event native.StreamEvent) error {
		published = append(published, event.Type)
		return nil
	}
	if pressure := discussCompactableTokens(cmd.DiscussMessages); pressure >= hardCompactionThreshold(16000) {
		t.Fatalf("fixture pressure %d must stay below the old window-share backstop", pressure)
	}
	handle, err := service.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for event := range handle.Events() {
		kinds = append(kinds, event.Kind)
	}
	for err := range handle.Errs() {
		t.Errorf("successful compaction emitted a turn error: %v", err)
	}
	if len(compactor.configs) != 1 {
		t.Fatalf("compaction calls = %d, want 1 after actual provider-budget rejection; events=%v", len(compactor.configs), kinds)
	}
	if cfg := compactor.configs[0]; cfg.TotalInputTokens != 3740 || cfg.TargetTokens != 399 || !cfg.HardPressure || cfg.SessionID != cmd.ThreadID {
		t.Fatalf("compaction config = %#v, want total pressure=3740, target=399 from history allowance=998 and hard pressure in the same session", cfg)
	}
	if len(kinds) != 2 || kinds[0] != turn.DiscussEventRunResolved || kinds[1] != turn.DiscussEventRecompose {
		t.Fatalf("transport events = %v, want resolved then recompose", kinds)
	}
	if len(published) != 0 || provider.callCount() != 0 || resolver.storeCalls != 0 {
		t.Fatalf("recompose leaked public events=%v provider=%d history=%d", published, provider.callCount(), resolver.storeCalls)
	}
	if len(runtime.finishes) != 1 || runtime.finishes[0].status != "" || runtime.finishes[0].message != "" {
		t.Fatalf("runtime finishes = %#v, want a clean recompose finish", runtime.finishes)
	}
	assertDeferredLifecycleRow(t, lifecycles.creates, handle.RunID(), contextLifecycleStatusCompleted, "")
}

func TestDiscussProviderBudgetRecoveryKeepsUnresolvedOverflowFailClosed(t *testing.T) {
	for _, scenario := range []string{"protected hook", "off", "compaction failure", "compaction noop"} {
		t.Run(scenario, func(t *testing.T) {
			service, resolver, provider, compactor, cmd := discussBudgetRecoveryFixture(t)
			wantCompactions := 0
			switch scenario {
			case "protected hook":
				resolver.resolveResult.RunConfig.ContextSourceFrags = []contextfrag.ContextFrag{contextfrag.MessageFrag(contextfrag.MessageFragInput{
					ID: "hook", Message: sdk.UserMessage(strings.Repeat("hook", 800)),
					Kind: contextfrag.KindHookContext, Slot: contextfrag.SlotAfterHistoryBeforeCurrent,
					Budget: contextfrag.BudgetPolicy{Overflow: contextfrag.OverflowKeep},
				})}
			case "off":
				service.SetSyncCompactionMode(scenario)
			case "compaction failure":
				compactor.err = errors.New("private summarizer failure")
				wantCompactions = 1
			case "compaction noop":
				compactor.status = compaction.StatusNoop
				wantCompactions = 1
			}
			_, lifecycles := configureDiscussLifecycle(service)
			handle, err := service.StartTurn(context.Background(), cmd)
			if err != nil {
				t.Fatal(err)
			}
			for event := range handle.Events() {
				if event.Kind == turn.DiscussEventRecompose || event.Kind == string(native.EventContextRecompose) {
					t.Fatalf("unresolved overflow emitted %s", event.Kind)
				}
			}
			var failures []error
			for err := range handle.Errs() {
				failures = append(failures, err)
			}
			if len(failures) != 1 || apperror.CodeOf(failures[0]) != apperror.CodeContextProtectedOverflow {
				t.Fatalf("errors = %v, want original protected overflow", failures)
			}
			if len(compactor.configs) != wantCompactions || provider.callCount() != 0 || resolver.storeCalls != 0 {
				t.Fatalf("compaction/provider/history calls = %d/%d/%d, want %d/0/0", len(compactor.configs), provider.callCount(), resolver.storeCalls, wantCompactions)
			}
			assertDeferredLifecycleRow(t, lifecycles.creates, handle.RunID(), contextLifecycleStatusFailedBudget, string(apperror.CodeContextProtectedOverflow))
		})
	}
}

func TestDiscussProviderBudgetRecoveryCompactsOrdinaryHistoryBeforeTrimming(t *testing.T) {
	service, resolver, provider, compactor, cmd := discussBudgetRecoveryFixture(t)
	configureDiscussLifecycle(service)
	cmd.DiscussMessages = cmd.DiscussMessages[20:]
	handle, err := service.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	recomposed := false
	for event := range handle.Events() {
		recomposed = recomposed || event.Kind == turn.DiscussEventRecompose
	}
	for err := range handle.Errs() {
		t.Errorf("ordinary history recovery failed: %v", err)
	}
	if !recomposed || len(compactor.configs) != 1 || provider.callCount() != 0 || resolver.storeCalls != 0 {
		t.Fatalf("recompose=%t compaction/provider/history=%d/%d/%d, want recovery before trimmed provider dispatch", recomposed, len(compactor.configs), provider.callCount(), resolver.storeCalls)
	}
	if cfg := compactor.configs[0]; cfg.TotalInputTokens != 2500 || cfg.TargetTokens != 399 || !cfg.HardPressure {
		t.Fatalf("ordinary history recovery config = %#v, want raw=2500 target=399 hard pressure", cfg)
	}
}

func TestDiscussProviderBudgetRecoveryLeavesFittingHistoryUnchanged(t *testing.T) {
	service, resolver, _, compactor, cmd := discussBudgetRecoveryFixture(t)
	configureDiscussLifecycle(service)
	provider := &triggerLifecycleProvider{}
	resolver.resolveResult.RunConfig.Model.Provider = provider
	history, current := strings.Repeat("h", 160), strings.Repeat("c", 1600)
	cmd.DiscussMessages = []turn.DiscussMessage{
		{Role: "assistant", Content: history},
		{Role: "user", Content: current},
	}
	handle, err := service.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	for event := range handle.Events() {
		if event.Kind == turn.DiscussEventRecompose {
			t.Error("fitting history requested unnecessary compaction")
		}
	}
	for err := range handle.Errs() {
		t.Errorf("fitting history turn failed: %v", err)
	}
	if len(compactor.configs) != 0 || provider.callCount() != 1 || resolver.storeCalls != 1 {
		t.Fatalf("compaction/provider/history=%d/%d/%d, want 0/1/1 for 50 history tokens within the final 500-token allowance", len(compactor.configs), provider.callCount(), resolver.storeCalls)
	}
	assertSDKMessagesEqual(t, provider.params.Messages, []sdk.Message{sdk.AssistantMessage(history), sdk.UserMessage(current)})
}

func TestDiscussProviderBudgetRecoveryFusesSummaryOnlyHistory(t *testing.T) {
	service, resolver, provider, compactor, cmd := discussBudgetRecoveryFixture(t)
	configureDiscussLifecycle(service)
	cmd.DiscussMessages = append(cmd.DiscussMessages[:20], cmd.DiscussMessages[len(cmd.DiscussMessages)-1])
	handle, err := service.StartTurn(t.Context(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	recomposed := false
	for event := range handle.Events() {
		recomposed = recomposed || event.Kind == turn.DiscussEventRecompose
	}
	for err := range handle.Errs() {
		t.Error(err)
	}
	if !recomposed || len(compactor.configs) != 1 || provider.callCount() != 0 || resolver.storeCalls != 0 {
		t.Fatalf("summary-only recovery: recompose=%v compactions=%d provider=%d stored=%d", recomposed, len(compactor.configs), provider.callCount(), resolver.storeCalls)
	}
}
